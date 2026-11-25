package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic calls the Messages API.
type Anthropic struct {
	BaseURL string // default https://api.anthropic.com
	APIKey  string
	Model   string
	Client  *http.Client
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    Role             `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicResponse struct {
	Content []anthropicBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements Model.
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	body := anthropicRequest{
		Model:     a.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toAnthropic(m))
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool(t))
	}

	header := http.Header{}
	header.Set("x-api-key", a.APIKey)
	header.Set("anthropic-version", "2023-06-01")

	var out anthropicResponse
	if err := postJSON(ctx, httpClient(a.Client), baseURL(a.BaseURL, "https://api.anthropic.com")+"/v1/messages", header, body, &out); err != nil {
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}

	msg := Message{Role: Assistant}
	var text []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			msg.Calls = append(msg.Calls, Call{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	msg.Text = strings.Join(text, "\n")
	return Response{
		Message: msg,
		Usage:   Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
	}, nil
}

func toAnthropic(m Message) anthropicMessage {
	out := anthropicMessage{Role: m.Role}
	for _, r := range m.Results {
		out.Content = append(out.Content, anthropicBlock{
			Type: "tool_result", ToolUseID: r.CallID, Content: r.Text, IsError: r.IsError,
		})
	}
	if m.Text != "" {
		out.Content = append(out.Content, anthropicBlock{Type: "text", Text: m.Text})
	}
	for _, c := range m.Calls {
		args := c.Args
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		out.Content = append(out.Content, anthropicBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: args})
	}
	return out
}

func baseURL(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	return strings.TrimRight(configured, "/")
}

func httpClient(c *http.Client) *http.Client {
	if c == nil {
		return http.DefaultClient
	}
	return c
}
