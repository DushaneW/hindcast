package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DushaneW/Hindcast/internal/investigate"
	"github.com/DushaneW/Hindcast/internal/llm"
	"github.com/DushaneW/Hindcast/internal/tool"
)

// Judge grades a report's root cause against a scenario's reference with a
// model. A judge is itself a model with its own errors: check its verdicts on
// a sample by hand before trusting its pass rate, and report which model
// judged.
type Judge struct {
	Model llm.Model
}

// Verdict is a judge's decision.
type Verdict struct {
	Correct bool   `json:"correct"`
	Reason  string `json:"reason"`
}

const judgePrompt = `You grade root cause analyses of production incidents.

You get the reference root cause, written by the person who knows what happened, and a candidate report. Decide whether the candidate identifies the same root cause.

Correct means the same underlying cause, even if worded differently or with less detail. Not correct: naming only a symptom (for example "pods are crash looping" when the cause is a lowered memory limit), naming a different cause, hedging between several causes without committing, or "unknown". Ignore the confidence level and writing quality.

Answer by calling grade.`

var gradeTool = tool.Spec{
	Name:        "grade",
	Description: "Record the verdict.",
	InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "correct": {"type": "boolean"},
    "reason": {"type": "string", "description": "One sentence."}
  },
  "required": ["correct", "reason"]
}`),
}

// Grade asks the judge model for a verdict.
func (j *Judge) Grade(ctx context.Context, reference string, rep *investigate.Report) (Verdict, error) {
	prompt := fmt.Sprintf("Reference root cause:\n%s\n\nCandidate root cause:\n%s\n\nCandidate summary:\n%s",
		reference, rep.RootCause, rep.Summary)
	resp, err := j.Model.Complete(ctx, llm.Request{
		System:    judgePrompt,
		Messages:  []llm.Message{{Role: llm.User, Text: prompt}},
		Tools:     []tool.Spec{gradeTool},
		MaxTokens: 512,
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: %w", err)
	}
	for _, c := range resp.Message.Calls {
		if c.Name != gradeTool.Name {
			continue
		}
		var v struct {
			Correct *bool  `json:"correct"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal(c.Args, &v); err != nil {
			return Verdict{}, fmt.Errorf("judge: invalid grade arguments: %w", err)
		}
		if v.Correct == nil {
			return Verdict{}, errors.New("judge: grade is missing correct")
		}
		return Verdict{Correct: *v.Correct, Reason: v.Reason}, nil
	}
	return Verdict{}, errors.New("judge: model did not call grade")
}
