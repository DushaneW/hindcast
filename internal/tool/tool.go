// Package tool defines the contract between the investigator and whatever
// serves tool calls: live MCP servers or a recorded trace.
package tool

import (
	"context"
	"encoding/json"
)

// Spec describes a tool offered to the model.
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Result is what a tool returned. IsError marks a failure the tool itself
// reported; the model sees it and can react.
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Box serves tool calls. Call returns an error only for failures outside the
// tool (dead transport, cancelled context), never for tool-level errors.
type Box interface {
	Tools() []Spec
	Call(ctx context.Context, name string, args json.RawMessage) (Result, error)
}
