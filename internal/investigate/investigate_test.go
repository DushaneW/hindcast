package investigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DushaneW/Hindcast/internal/alert"
	"github.com/DushaneW/Hindcast/internal/llm"
	"github.com/DushaneW/Hindcast/internal/tool"
	"github.com/DushaneW/Hindcast/internal/trace"
)

// scripted answers each model turn with the next function in turns.
type scripted struct {
	turns []func(llm.Request) llm.Message
	reqs  []llm.Request
}

func (s *scripted) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	s.reqs = append(s.reqs, req)
	if len(s.reqs) > len(s.turns) {
		return llm.Response{}, errors.New("script exhausted")
	}
	msg := s.turns[len(s.reqs)-1](req)
	msg.Role = llm.Assistant
	return llm.Response{Message: msg, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
}

func call(id, name, args string) llm.Message {
	return llm.Message{Calls: []llm.Call{{ID: id, Name: name, Args: json.RawMessage(args)}}}
}

func submit(id, args string) func(llm.Request) llm.Message {
	return func(llm.Request) llm.Message { return call(id, submitTool, args) }
}

// fakeBox answers every tool with "<name> ok" and records the calls.
type fakeBox struct {
	mu    sync.Mutex
	calls []string
	reply func(name string, args json.RawMessage) tool.Result
}

func (b *fakeBox) Tools() []tool.Spec {
	return []tool.Spec{{Name: "k8s__events"}, {Name: "k8s__logs"}}
}

func (b *fakeBox) Call(_ context.Context, name string, args json.RawMessage) (tool.Result, error) {
	b.mu.Lock()
	b.calls = append(b.calls, name+" "+string(args))
	b.mu.Unlock()
	if b.reply != nil {
		return b.reply(name, args), nil
	}
	return tool.Result{Text: name + " ok"}, nil
}

var payload = alert.Payload{Alerts: []alert.Alert{{
	Status:   "firing",
	Labels:   map[string]string{"alertname": "KubePodCrashLooping", "namespace": "shop", "pod": "api-7d9"},
	StartsAt: time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC),
}}}

func lastResults(req llm.Request) []llm.Result {
	return req.Messages[len(req.Messages)-1].Results
}

func TestRunCitesRealCalls(t *testing.T) {
	model := &scripted{turns: []func(llm.Request) llm.Message{
		func(llm.Request) llm.Message { return call("m1", "k8s__events", `{"namespace":"shop"}`) },
		func(req llm.Request) llm.Message {
			res := lastResults(req)
			if len(res) != 1 || res[0].CallID != "m1" || !strings.HasPrefix(res[0].Text, "[call c1]\n") {
				t.Errorf("tool result = %+v", res)
			}
			return call("m2", submitTool, `{
				"summary": "OOM after deploy", "root_cause": "memory limit lowered", "confidence": "high",
				"evidence": [{"call_id": "c1", "finding": "OOMKilled events"}, {"call_id": "c9", "finding": "made up"}]
			}`)
		},
	}}
	inv := &Investigator{Model: model, Tools: &fakeBox{}}
	rep, err := inv.Run(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}

	if rep.Alert != "KubePodCrashLooping" || rep.RootCause != "memory limit lowered" || rep.Confidence != High {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Evidence) != 1 || rep.Evidence[0] != (Evidence{CallID: "c1", Tool: "k8s__events", Finding: "OOMKilled events"}) {
		t.Errorf("evidence = %+v", rep.Evidence)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], `"c9"`) {
		t.Errorf("warnings = %v", rep.Warnings)
	}
	if rep.Stats.Steps != 2 || rep.Stats.ToolCalls != 1 || rep.Stats.InputTokens != 200 {
		t.Errorf("stats = %+v", rep.Stats)
	}
	tools := model.reqs[0].Tools
	if tools[len(tools)-1].Name != submitTool {
		t.Error("submit_report must be offered")
	}
}

func TestConfidenceLoweredWithoutEvidence(t *testing.T) {
	model := &scripted{turns: []func(llm.Request) llm.Message{
		submit("m1", `{"summary": "s", "root_cause": "dns", "confidence": "high", "evidence": []}`),
	}}
	rep, err := (&Investigator{Model: model, Tools: &fakeBox{}}).Run(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Confidence != Low || len(rep.Warnings) != 1 {
		t.Errorf("confidence = %s, warnings = %v", rep.Confidence, rep.Warnings)
	}
}

func TestInvalidSubmissionIsReturnedToModel(t *testing.T) {
	model := &scripted{turns: []func(llm.Request) llm.Message{
		submit("m1", `{"summary": "s", "root_cause": "dns", "confidence": "certain"}`),
		func(req llm.Request) llm.Message {
			res := lastResults(req)
			if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Text, "confidence") {
				t.Errorf("rejection = %+v", res)
			}
			return call("m2", submitTool, `{"summary": "s", "root_cause": "unknown", "confidence": "low", "evidence": []}`)
		},
	}}
	rep, err := (&Investigator{Model: model, Tools: &fakeBox{}}).Run(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RootCause != "unknown" {
		t.Errorf("report = %+v", rep)
	}
}

func TestBudget(t *testing.T) {
	loop := func(llm.Request) llm.Message { return call("m", "k8s__logs", `{}`) }
	model := &scripted{turns: []func(llm.Request) llm.Message{loop, loop, loop}}
	_, err := (&Investigator{Model: model, Tools: &fakeBox{}, Budget: Budget{MaxSteps: 3}}).Run(context.Background(), payload)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	last := model.reqs[2].Messages
	if !strings.Contains(last[len(last)-1].Text, "last turn") {
		t.Error("the final turn should tell the model to submit")
	}
}

func TestNudgeOnce(t *testing.T) {
	talk := func(llm.Request) llm.Message { return llm.Message{Text: "I think it is DNS."} }
	model := &scripted{turns: []func(llm.Request) llm.Message{talk, talk}}
	_, err := (&Investigator{Model: model, Tools: &fakeBox{}}).Run(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "without calling submit_report") {
		t.Errorf("err = %v", err)
	}
	if len(model.reqs) != 2 {
		t.Errorf("model called %d times, want 2", len(model.reqs))
	}
}

func TestPrefetch(t *testing.T) {
	box := &fakeBox{}
	model := &scripted{turns: []func(llm.Request) llm.Message{
		func(req llm.Request) llm.Message {
			if len(req.Messages) != 3 || len(req.Messages[1].Calls) != 2 || len(req.Messages[2].Results) != 2 {
				t.Fatalf("prefetch should add one assistant turn and its results, got %+v", req.Messages)
			}
			return call("m1", submitTool, `{"summary": "s", "root_cause": "x", "confidence": "low", "evidence": [{"call_id": "c2", "finding": "f"}]}`)
		},
	}}
	inv := &Investigator{Model: model, Tools: box, Prefetch: []PrefetchCall{
		{Tool: "k8s__events", Args: map[string]any{"namespace": "{{.Labels.namespace}}", "since": `{{rfc3339 (before .StartsAt "30m")}}`}},
		{Tool: "k8s__logs", Args: map[string]any{"pod": "{{.Labels.pod}}", "tail": 200}},
		{Tool: "k8s__logs", Args: map[string]any{"container": "{{.Labels.container}}"}},
	}}
	rep, err := inv.Run(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		`k8s__events {"namespace":"shop","since":"2026-11-20T02:30:00Z"}`: true,
		`k8s__logs {"pod":"api-7d9","tail":200}`:                          true,
	}
	if len(box.calls) != 2 || !want[box.calls[0]] || !want[box.calls[1]] {
		t.Errorf("calls = %v", box.calls)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "container") {
		t.Errorf("a prefetch call with a missing label should be skipped with a warning: %v", rep.Warnings)
	}
	if len(rep.Evidence) != 1 || rep.Evidence[0].Tool != "k8s__logs" {
		t.Errorf("prefetch calls must be citable: %+v", rep.Evidence)
	}
}

func TestLongResultsAreClipped(t *testing.T) {
	box := &fakeBox{reply: func(string, json.RawMessage) tool.Result {
		return tool.Result{Text: strings.Repeat("é", 100)} // 200 bytes
	}}
	var shown string
	model := &scripted{turns: []func(llm.Request) llm.Message{
		func(llm.Request) llm.Message { return call("m1", "k8s__logs", `{}`) },
		func(req llm.Request) llm.Message {
			shown = lastResults(req)[0].Text
			return call("m2", submitTool, `{"summary": "s", "root_cause": "x", "confidence": "low", "evidence": []}`)
		},
	}}
	var buf bytes.Buffer
	inv := &Investigator{Model: model, Tools: box, Trace: trace.NewWriter(&buf), Budget: Budget{MaxResultBytes: 51}}
	if _, err := inv.Run(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, "[truncated: showing 50 of 200 bytes") {
		t.Errorf("shown = %q", shown)
	}

	tr, err := trace.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Calls) != 1 || len(tr.Calls[0].Result.Text) != 200 {
		t.Error("the trace must keep the full result")
	}
	if tr.Report == nil {
		t.Error("the trace must end with the report")
	}
}

func TestTraceReplays(t *testing.T) {
	var buf bytes.Buffer
	turns := []func(llm.Request) llm.Message{
		func(llm.Request) llm.Message { return call("m1", "k8s__events", `{"namespace":"shop"}`) },
		submit("m2", `{"summary": "s", "root_cause": "x", "confidence": "medium", "evidence": [{"call_id": "c2", "finding": "f"}]}`),
	}
	live := &Investigator{Model: &scripted{turns: turns}, Tools: &fakeBox{}, Trace: trace.NewWriter(&buf),
		Prefetch: []PrefetchCall{{Tool: "k8s__logs", Args: map[string]any{"pod": "{{.Labels.pod}}", "tail": 200}}}}
	if _, err := live.Run(context.Background(), payload); err != nil {
		t.Fatal(err)
	}

	tr, err := trace.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	replayer, err := trace.NewReplayer(tr)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := alert.Parse(tr.Header.Alert)
	if err != nil {
		t.Fatal(err)
	}
	prefetch, err := PrefetchFromTrace(tr)
	if err != nil {
		t.Fatal(err)
	}
	var prefetched, seen string
	replayTurns := []func(llm.Request) llm.Message{
		func(req llm.Request) llm.Message {
			prefetched = lastResults(req)[0].Text
			return turns[0](req)
		},
		func(req llm.Request) llm.Message {
			seen = lastResults(req)[0].Text
			return turns[1](req)
		},
	}
	rep, err := (&Investigator{Model: &scripted{turns: replayTurns}, Tools: replayer, Prefetch: prefetch}).Run(context.Background(), recorded)
	if err != nil {
		t.Fatal(err)
	}
	if prefetched != "[call c1]\nk8s__logs ok" {
		t.Errorf("replay must start from the recorded prefetch, got %q", prefetched)
	}
	if seen != "[call c2]\nk8s__events ok" || rep.Confidence != Medium {
		t.Errorf("replayed result = %q, report = %+v", seen, rep)
	}
}

func TestPrefetchFromTraceUsesIssueOrder(t *testing.T) {
	tr := &trace.Trace{Calls: []trace.Call{
		{ID: "c10", Tool: "late", Args: json.RawMessage(`{}`), Prefetch: true},
		{ID: "c2", Tool: "second", Args: json.RawMessage(`{"a":1}`), Prefetch: true},
		{ID: "c11", Tool: "model_call"},
		{ID: "c1", Tool: "first", Prefetch: true},
	}}
	got, err := PrefetchFromTrace(tr)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, pc := range got {
		order = append(order, pc.Tool)
	}
	if strings.Join(order, ",") != "first,second,late" {
		t.Errorf("order = %v", order)
	}
	if got[0].Args == nil || !got[0].Literal {
		t.Errorf("recorded calls without arguments must replay as {} literally: %+v", got[0])
	}
}
