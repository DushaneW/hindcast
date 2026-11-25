// Package llm is a provider-neutral chat interface with tool calling, plus
// clients for the Anthropic Messages API and OpenAI-compatible chat
// completions (OpenAI, Ollama, vLLM, OpenRouter, ...).
package llm

import (
	"context"
	"encoding/json"

	"github.com/DushaneW/Hindcast/internal/tool"
)

// Role of a message author.
type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is one turn. An assistant message may carry Calls; the user message
// after it carries a Result for every call.
type Message struct {
	Role    Role
	Text    string
	Calls   []Call
	Results []Result
}

// Call is a tool call requested by the model.
type Call struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Result answers the Call with the same ID.
type Result struct {
	CallID  string
	Text    string
	IsError bool
}

// Request is one completion request.
type Request struct {
	System    string
	Messages  []Message
	Tools     []tool.Spec
	MaxTokens int
}

// Usage is the token count reported by the provider.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is the model's reply.
type Response struct {
	Message Message
	Usage   Usage
}

// Model completes a conversation.
type Model interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
