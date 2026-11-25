package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DushaneW/Hindcast/internal/tool"
)

func sampleTrace(t *testing.T) *Trace {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	must(t, w.WriteHeader(Header{Alert: json.RawMessage(`{"alerts":[]}`), Tools: []tool.Spec{{Name: "logs", InputSchema: json.RawMessage(`{"type":"object"}`)}}}))
	must(t, w.WriteCall(Call{ID: "c1", Tool: "logs", Args: json.RawMessage(`{"pod":"api-1","since":"30m"}`), Result: tool.Result{Text: "OOMKilled"}}))
	must(t, w.WriteCall(Call{ID: "c2", Tool: "logs", Args: json.RawMessage(`{"pod":"api-2"}`), Error: "connection reset"}))
	must(t, w.WriteReport(map[string]string{"root_cause": "memory limit"}))

	tr, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	tr := sampleTrace(t)
	if tr.Header.Version != Version || len(tr.Header.Tools) != 1 {
		t.Errorf("header = %+v", tr.Header)
	}
	if len(tr.Calls) != 2 || tr.Calls[0].Result.Text != "OOMKilled" || tr.Calls[1].Error != "connection reset" {
		t.Errorf("calls = %+v", tr.Calls)
	}
	if !strings.Contains(string(tr.Report), "memory limit") {
		t.Errorf("report = %s", tr.Report)
	}
}

func TestReadRejects(t *testing.T) {
	for name, input := range map[string]string{
		"no header":      `{"type":"call","id":"c1"}`,
		"bad version":    `{"type":"header","version":99}`,
		"unknown record": "{\"type\":\"header\",\"version\":1}\n{\"type\":\"nope\"}",
		"empty":          "",
	} {
		if _, err := Read(strings.NewReader(input)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestReplayer(t *testing.T) {
	r, err := NewReplayer(sampleTrace(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Key order and whitespace do not matter.
	res, err := r.Call(ctx, "logs", json.RawMessage(`{ "since": "30m", "pod": "api-1" }`))
	if err != nil || res.IsError || res.Text != "OOMKilled" {
		t.Errorf("matching call: %+v, %v", res, err)
	}

	res, _ = r.Call(ctx, "logs", json.RawMessage(`{"pod":"api-2"}`))
	if !res.IsError || !strings.Contains(res.Text, "connection reset") {
		t.Errorf("recorded failure should replay as an error result: %+v", res)
	}

	res, _ = r.Call(ctx, "logs", json.RawMessage(`{"pod":"api-3"}`))
	if !res.IsError || !strings.Contains(res.Text, `{"pod":"api-1","since":"30m"}`) {
		t.Errorf("unmatched call should list recorded arguments: %+v", res)
	}

	res, _ = r.Call(ctx, "metrics", nil)
	if !res.IsError || !strings.Contains(res.Text, "was not called") {
		t.Errorf("unknown tool: %+v", res)
	}
}

func TestCanonical(t *testing.T) {
	got, err := Canonical(json.RawMessage(`{"b":1.50,"a":[{"d":1,"c":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":[{"c":2,"d":1}],"b":1.50}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, _ := Canonical(nil); got != "{}" {
		t.Errorf("empty input: %s", got)
	}
	if _, err := Canonical(json.RawMessage(`{} {}`)); err == nil {
		t.Error("trailing data should be rejected")
	}
}
