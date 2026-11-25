package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DushaneW/Hindcast/internal/tool"
)

var conversation = Request{
	System: "be brief",
	Messages: []Message{
		{Role: User, Text: "why is api down?"},
		{Role: Assistant, Text: "checking", Calls: []Call{{ID: "t1", Name: "logs", Args: json.RawMessage(`{"pod":"api"}`)}}},
		{Role: User, Results: []Result{{CallID: "t1", Text: "OOMKilled", IsError: true}}},
	},
	Tools:     []tool.Spec{{Name: "logs", Description: "pod logs", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	MaxTokens: 100,
}

// serve returns a server that records the request body and answers with reply.
func serve(t *testing.T, reply string, got *map[string]any, headers *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, got); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		*headers = r.Header.Clone()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonEq(t *testing.T, got any, want string) {
	t.Helper()
	g, _ := json.Marshal(got)
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	ws, _ := json.Marshal(w)
	if string(g) != string(ws) {
		t.Errorf("got  %s\nwant %s", g, ws)
	}
}

func TestAnthropic(t *testing.T) {
	var body map[string]any
	var headers http.Header
	srv := serve(t, `{
		"content": [
			{"type": "text", "text": "memory"},
			{"type": "tool_use", "id": "t2", "name": "events", "input": {"ns": "prod"}}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`, &body, &headers)

	m := &Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "claude"}
	resp, err := m.Complete(context.Background(), conversation)
	if err != nil {
		t.Fatal(err)
	}

	if headers.Get("x-api-key") != "k" || headers.Get("anthropic-version") == "" {
		t.Errorf("headers = %v", headers)
	}
	jsonEq(t, body["messages"], `[
		{"role": "user", "content": [{"type": "text", "text": "why is api down?"}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "checking"},
			{"type": "tool_use", "id": "t1", "name": "logs", "input": {"pod": "api"}}
		]},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "OOMKilled", "is_error": true}]}
	]`)
	jsonEq(t, body["tools"], `[{"name": "logs", "description": "pod logs", "input_schema": {"type": "object"}}]`)
	if body["system"] != "be brief" || body["model"] != "claude" {
		t.Errorf("body = %v", body)
	}

	if resp.Message.Text != "memory" || len(resp.Message.Calls) != 1 || resp.Message.Calls[0].Name != "events" {
		t.Errorf("message = %+v", resp.Message)
	}
	if string(resp.Message.Calls[0].Args) != `{"ns": "prod"}` {
		t.Errorf("args = %s", resp.Message.Calls[0].Args)
	}
	if resp.Usage != (Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAI(t *testing.T) {
	var body map[string]any
	var headers http.Header
	srv := serve(t, `{
		"choices": [{"message": {"role": "assistant", "content": null, "tool_calls": [
			{"id": "t2", "type": "function", "function": {"name": "events", "arguments": "{\"ns\":\"prod\"}"}}
		]}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`, &body, &headers)

	m := &OpenAI{BaseURL: srv.URL, Model: "llama"}
	resp, err := m.Complete(context.Background(), conversation)
	if err != nil {
		t.Fatal(err)
	}

	if headers.Get("Authorization") != "" {
		t.Error("no key configured, so no Authorization header expected")
	}
	jsonEq(t, body["messages"], `[
		{"role": "system", "content": "be brief"},
		{"role": "user", "content": "why is api down?"},
		{"role": "assistant", "content": "checking", "tool_calls": [
			{"id": "t1", "type": "function", "function": {"name": "logs", "arguments": "{\"pod\":\"api\"}"}}
		]},
		{"role": "tool", "tool_call_id": "t1", "content": "ERROR: OOMKilled"}
	]`)
	jsonEq(t, body["tools"], `[{"type": "function", "function": {"name": "logs", "description": "pod logs", "parameters": {"type": "object"}}}]`)

	if len(resp.Message.Calls) != 1 || string(resp.Message.Calls[0].Args) != `{"ns":"prod"}` {
		t.Errorf("message = %+v", resp.Message)
	}
	if resp.Usage != (Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestRetry(t *testing.T) {
	initialBackoff = time.Millisecond
	t.Cleanup(func() { initialBackoff = time.Second })

	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"content": [{"type": "text", "text": "ok"}]}`)
	}))
	defer srv.Close()

	resp, err := (&Anthropic{BaseURL: srv.URL}).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || resp.Message.Text != "ok" {
		t.Errorf("attempts = %d, text = %q", attempts, resp.Message.Text)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := (&OpenAI{BaseURL: srv.URL}).Complete(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "401") || attempts != 1 {
		t.Errorf("err = %v, attempts = %d", err, attempts)
	}
}
