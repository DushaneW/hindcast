package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DushaneW/Hindcast/internal/investigate"
	"github.com/DushaneW/Hindcast/internal/llm"
)

// twoStep checks events, then submits rootCause citing that call.
type twoStep struct {
	rootCause string
	turn      int
}

func (m *twoStep) Complete(context.Context, llm.Request) (llm.Response, error) {
	m.turn++
	c := llm.Call{ID: "t1", Name: "k8s__events_list", Args: json.RawMessage(`{"namespace": "shop"}`)}
	if m.turn > 1 {
		report, _ := json.Marshal(map[string]any{
			"summary": "s", "root_cause": m.rootCause, "confidence": "high",
			"evidence": []map[string]string{{"call_id": "c1", "finding": "OOMKilled events"}},
		})
		c = llm.Call{ID: "t2", Name: "submit_report", Args: report}
	}
	return llm.Response{Message: llm.Message{Role: llm.Assistant, Calls: []llm.Call{c}}}, nil
}

func TestShippedScenariosLoad(t *testing.T) {
	scenarios, err := Load("../../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) < 3 {
		t.Errorf("loaded %d scenarios", len(scenarios))
	}
	for _, s := range scenarios {
		if len(s.trace.Calls) == 0 || len(s.trace.Header.Tools) == 0 {
			t.Errorf("%s: empty trace", s.Name)
		}
	}
}

func TestRun(t *testing.T) {
	scenarios, err := Load("../../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	var oom *Scenario
	for _, s := range scenarios {
		if s.Name == "oom-after-limit-change" {
			oom = s
		}
	}
	if oom == nil {
		t.Fatal("oom-after-limit-change scenario missing")
	}

	res := Runner{Model: &twoStep{rootCause: "Commit 5d0f7b4 cut the memory limit from 512Mi to 256Mi"}}.Run(context.Background(), oom, nil)
	if !res.Pass || res.Error != "" {
		t.Errorf("correct answer failed: %+v", res)
	}

	res = Runner{Model: &twoStep{rootCause: "grpc-go upgrade introduced a leak"}}.Run(context.Background(), oom, nil)
	if res.Pass || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "matches none") {
		t.Errorf("wrong answer passed: %+v", res)
	}
}

// fixedJudge answers grade with a fixed verdict and records the prompt.
type fixedJudge struct {
	correct bool
	prompt  string
}

func (j *fixedJudge) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	j.prompt = req.Messages[0].Text
	args, _ := json.Marshal(map[string]any{"correct": j.correct, "reason": "because"})
	return llm.Response{Message: llm.Message{Role: llm.Assistant, Calls: []llm.Call{{ID: "g", Name: "grade", Args: args}}}}, nil
}

func loadOOM(t *testing.T) *Scenario {
	t.Helper()
	scenarios, err := Load("../../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scenarios {
		if s.Name == "oom-after-limit-change" {
			return s
		}
	}
	t.Fatal("oom-after-limit-change scenario missing")
	return nil
}

func TestJudgeReplacesRegexp(t *testing.T) {
	oom := loadOOM(t)
	if oom.Expect.Reference == "" {
		t.Fatal("scenario needs a reference for this test")
	}

	// A phrasing the regexps reject, accepted by the judge.
	judge := &fixedJudge{correct: true}
	res := Runner{Model: &twoStep{rootCause: "the container now gets half the RAM it used to"}, Judge: &Judge{Model: judge}}.Run(context.Background(), oom, nil)
	if !res.Pass || res.Verdict == nil || !res.Verdict.Correct {
		t.Errorf("judged correct but failed: %+v", res)
	}
	if !strings.Contains(judge.prompt, oom.Expect.Reference) || !strings.Contains(judge.prompt, "half the RAM") {
		t.Errorf("judge prompt lacks reference or candidate:\n%s", judge.prompt)
	}

	res = Runner{Model: &twoStep{rootCause: "Commit 5d0f7b4 cut the memory limit"}, Judge: &Judge{Model: &fixedJudge{correct: false}}}.Run(context.Background(), oom, nil)
	if res.Pass || len(res.Failures) != 1 || !strings.HasPrefix(res.Failures[0], "judge:") {
		t.Errorf("judged wrong but passed: %+v", res)
	}
}

func TestJudgeMustCallGrade(t *testing.T) {
	silent := scriptedText("looks right to me")
	_, err := (&Judge{Model: silent}).Grade(context.Background(), "ref", &investigate.Report{RootCause: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not call grade") {
		t.Errorf("err = %v", err)
	}
}

type scriptedText string

func (s scriptedText) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.Assistant, Text: string(s)}}, nil
}
