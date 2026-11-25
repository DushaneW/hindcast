package investigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Confidence levels the model may report.
const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
)

// Report is the outcome of an investigation.
type Report struct {
	Alert      string     `json:"alert"`
	Summary    string     `json:"summary"`
	RootCause  string     `json:"root_cause"`
	Confidence string     `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
	NextSteps  []string   `json:"next_steps,omitempty"`
	// Warnings are problems Hindcast found with the investigation itself,
	// such as citations of calls that never happened.
	Warnings []string `json:"warnings,omitempty"`
	Stats    Stats    `json:"stats"`
}

// Evidence ties a finding to the tool call it came from.
type Evidence struct {
	CallID  string `json:"call_id"`
	Tool    string `json:"tool"`
	Finding string `json:"finding"`
}

// Stats describes the cost of an investigation.
type Stats struct {
	Steps        int           `json:"steps"`
	ToolCalls    int           `json:"tool_calls"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Duration     time.Duration `json:"duration_ns"`
}

const submitTool = "submit_report"

var submitSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "description": "Two or three sentences for the on-call engineer."},
    "root_cause": {"type": "string", "description": "The most likely root cause, or 'unknown' if the evidence does not support one."},
    "confidence": {"type": "string", "enum": ["low", "medium", "high"]},
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "call_id": {"type": "string", "description": "ID of the tool call, as shown in its result, e.g. c3."},
          "finding": {"type": "string", "description": "What that result shows."}
        },
        "required": ["call_id", "finding"]
      }
    },
    "next_steps": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "root_cause", "confidence", "evidence"]
}`)

type submission struct {
	Summary    string `json:"summary"`
	RootCause  string `json:"root_cause"`
	Confidence string `json:"confidence"`
	Evidence   []struct {
		CallID  string `json:"call_id"`
		Finding string `json:"finding"`
	} `json:"evidence"`
	NextSteps []string `json:"next_steps"`
}

// buildReport validates a submit_report call against the calls that were
// actually made. Citations of unknown calls are dropped with a warning, and a
// medium or high confidence claim without any valid citation is lowered.
func buildReport(args json.RawMessage, made map[string]string) (*Report, error) {
	var s submission
	if err := json.Unmarshal(args, &s); err != nil {
		return nil, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if strings.TrimSpace(s.RootCause) == "" || strings.TrimSpace(s.Summary) == "" {
		return nil, errors.New("summary and root_cause are required")
	}
	switch s.Confidence {
	case Low, Medium, High:
	default:
		return nil, fmt.Errorf("confidence must be low, medium or high, got %q", s.Confidence)
	}

	r := &Report{Summary: s.Summary, RootCause: s.RootCause, Confidence: s.Confidence, NextSteps: s.NextSteps}
	for _, e := range s.Evidence {
		toolName, ok := made[e.CallID]
		if !ok {
			r.Warnings = append(r.Warnings, fmt.Sprintf("dropped evidence citing unknown call %q: %s", e.CallID, e.Finding))
			continue
		}
		r.Evidence = append(r.Evidence, Evidence{CallID: e.CallID, Tool: toolName, Finding: e.Finding})
	}
	if len(r.Evidence) == 0 && r.Confidence != Low {
		r.Warnings = append(r.Warnings, fmt.Sprintf("confidence lowered from %s to low: no valid evidence cited", r.Confidence))
		r.Confidence = Low
	}
	return r, nil
}

// Markdown renders the report for humans.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", r.Alert)
	fmt.Fprintf(&b, "**Root cause** (%s confidence): %s\n\n", r.Confidence, r.RootCause)
	fmt.Fprintf(&b, "%s\n", r.Summary)
	if len(r.Evidence) > 0 {
		b.WriteString("\n## Evidence\n\n")
		for _, e := range r.Evidence {
			fmt.Fprintf(&b, "- `%s` %s: %s\n", e.CallID, e.Tool, e.Finding)
		}
	}
	if len(r.NextSteps) > 0 {
		b.WriteString("\n## Next steps\n\n")
		for _, s := range r.NextSteps {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\n---\n%d steps, %d tool calls, %d input / %d output tokens, %s\n",
		r.Stats.Steps, r.Stats.ToolCalls, r.Stats.InputTokens, r.Stats.OutputTokens, r.Stats.Duration.Round(time.Millisecond))
	return b.String()
}
