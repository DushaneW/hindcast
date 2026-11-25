package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hindcast/hindcast/internal/investigate"
	"github.com/hindcast/hindcast/internal/llm"
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

	res := Run(context.Background(), oom, &twoStep{rootCause: "Commit 5d0f7b4 cut the memory limit from 512Mi to 256Mi"}, investigate.Budget{}, nil)
	if !res.Pass || res.Error != "" {
		t.Errorf("correct answer failed: %+v", res)
	}

	res = Run(context.Background(), oom, &twoStep{rootCause: "grpc-go upgrade introduced a leak"}, investigate.Budget{}, nil)
	if res.Pass || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "matches none") {
		t.Errorf("wrong answer passed: %+v", res)
	}
}
