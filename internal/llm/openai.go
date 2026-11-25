package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// OpenAI calls an OpenAI-compatible /chat/completions endpoint. Point BaseURL
// at Ollama (http://localhost:11434/v1), vLLM, OpenRouter and so on; APIKey
// may be empty for local servers.
type OpenAI struct {
	BaseURL string // default https://api.openai.com/v1
	APIKey  string
	Model   string
	Client  *http.Client
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type openaiRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
}

type openaiResponse struct {
	Choices []struct {
		Message openaiMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete implements Model.
func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	body := openaiRequest{Model: o.Model, MaxTokens: req.MaxTokens}
	if req.System != "" {
		body.Messages = append(body.Messages, openaiMessage{Role: "system", Content: &req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toOpenAI(m)...)
	}
	for _, t := range req.Tools {
		var ot openaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		body.Tools = append(body.Tools, ot)
	}

	header := http.Header{}
	if o.APIKey != "" {
		header.Set("Authorization", "Bearer "+o.APIKey)
	}

	var out openaiResponse
	if err := postJSON(ctx, httpClient(o.Client), baseURL(o.BaseURL, "https://api.openai.com/v1")+"/chat/completions", header, body, &out); err != nil {
		return Response{}, fmt.Errorf("openai: %w", err)
	}
	if len(out.Choices) == 0 {
		return Response{}, errors.New("openai: response has no choices")
	}

	reply := out.Choices[0].Message
	msg := Message{Role: Assistant}
	if reply.Content != nil {
		msg.Text = *reply.Content
	}
	for _, tc := range reply.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if tc.Function.Arguments == "" {
			args = json.RawMessage("{}")
		}
		msg.Calls = append(msg.Calls, Call{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	return Response{
		Message: msg,
		Usage:   Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
	}, nil
}

// toOpenAI maps one neutral message to the wire format, where each tool
// result is a separate "tool" message.
func toOpenAI(m Message) []openaiMessage {
	var out []openaiMessage
	for _, r := range m.Results {
		text := r.Text
		if r.IsError {
			text = "ERROR: " + text
		}
		out = append(out, openaiMessage{Role: "tool", ToolCallID: r.CallID, Content: &text})
	}
	if m.Role == Assistant {
		msg := openaiMessage{Role: "assistant"}
		if m.Text != "" {
			text := m.Text
			msg.Content = &text
		}
		for _, c := range m.Calls {
			var tc openaiToolCall
			tc.ID = c.ID
			tc.Type = "function"
			tc.Function.Name = c.Name
			tc.Function.Arguments = string(c.Args)
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		return append(out, msg)
	}
	if m.Text != "" {
		text := m.Text
		out = append(out, openaiMessage{Role: "user", Content: &text})
	}
	return out
}
