// Package investigate runs the investigation loop: prefetch the context every
// alert needs, then let the model call tools until it submits a report.
package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/llm"
	"github.com/hindcast/hindcast/internal/tool"
	"github.com/hindcast/hindcast/internal/trace"
)

// ErrBudget is returned when the model does not submit a report within the
// step budget.
var ErrBudget = errors.New("step budget exhausted without a report")

// Budget bounds an investigation.
type Budget struct {
	MaxSteps       int           // model turns
	MaxTokens      int           // output tokens per model turn
	ToolTimeout    time.Duration // per tool call
	MaxResultBytes int           // tool output shown to the model; the trace keeps all of it
}

// DefaultBudget is used for zero fields.
var DefaultBudget = Budget{MaxSteps: 15, MaxTokens: 4096, ToolTimeout: 30 * time.Second, MaxResultBytes: 16 << 10}

// Investigator holds what an investigation needs. Trace may be nil.
type Investigator struct {
	Model    llm.Model
	Tools    tool.Box
	Trace    *trace.Writer
	Budget   Budget
	Prefetch []PrefetchCall
}

const systemPrompt = `You are investigating a production alert as an SRE.

You have read-only tools. Every tool result starts with a line like "[call c3]"; that is the ID you cite as evidence.

Work from evidence. State a hypothesis, then check it with a tool call before relying on it. Prefer narrow queries around the time the alert started. When a hypothesis is contradicted, drop it.

When you know the root cause, or have used what the tools can tell you, call submit_report. Cite the calls that support each finding. If the evidence does not establish a root cause, say root_cause is unknown and set confidence to low; a wrong confident answer costs the on-call engineer more than an honest unknown.`

type run struct {
	inv    *Investigator
	budget Budget
	mu     sync.Mutex
	made   map[string]string // call ID -> tool name
	next   int
	stats  Stats
	warn   []string
}

// Run investigates the alert payload and returns the report.
func (inv *Investigator) Run(ctx context.Context, p alert.Payload) (*Report, error) {
	start := time.Now()
	r := &run{inv: inv, budget: withDefaults(inv.Budget), made: make(map[string]string)}

	if inv.Trace != nil {
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("encode alert: %w", err)
		}
		if err := inv.Trace.WriteHeader(trace.Header{CreatedAt: start.UTC(), Alert: raw, Tools: inv.Tools.Tools()}); err != nil {
			return nil, err
		}
	}

	msgs := []llm.Message{{Role: llm.User, Text: "Investigate this alert.\n\n" + p.Describe()}}
	calls, warnings := expandPrefetch(inv.Prefetch, p.Primary())
	r.warn = warnings
	if len(calls) > 0 {
		results, err := r.execute(ctx, calls, true)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs,
			llm.Message{Role: llm.Assistant, Text: "Collecting standard context first.", Calls: calls},
			llm.Message{Role: llm.User, Results: results})
	}

	specs := append(slices.Clone(inv.Tools.Tools()), tool.Spec{
		Name:        submitTool,
		Description: "Finish the investigation with a root cause and the evidence for it.",
		InputSchema: submitSchema,
	})
	nudged := false
	for step := 1; step <= r.budget.MaxSteps; step++ {
		if step == r.budget.MaxSteps {
			msgs = appendText(msgs, "This is your last turn. Call submit_report now with what you have.")
		}
		resp, err := inv.Model.Complete(ctx, llm.Request{System: systemPrompt, Messages: msgs, Tools: specs, MaxTokens: r.budget.MaxTokens})
		if err != nil {
			return nil, err
		}
		r.stats.Steps = step
		r.stats.InputTokens += resp.Usage.InputTokens
		r.stats.OutputTokens += resp.Usage.OutputTokens
		msgs = append(msgs, resp.Message)

		if len(resp.Message.Calls) == 0 {
			if nudged {
				return nil, errors.New("model stopped without calling submit_report")
			}
			nudged = true
			msgs = append(msgs, llm.Message{Role: llm.User, Text: "Continue the investigation with tool calls, or call submit_report."})
			continue
		}

		var toolCalls []llm.Call
		var results []llm.Result
		for _, c := range resp.Message.Calls {
			if c.Name != submitTool {
				toolCalls = append(toolCalls, c)
				continue
			}
			rep, err := buildReport(c.Args, r.snapshot())
			if err != nil {
				results = append(results, llm.Result{CallID: c.ID, Text: "submit_report rejected: " + err.Error(), IsError: true})
				continue
			}
			return r.finish(rep, p, start)
		}
		executed, err := r.execute(ctx, toolCalls, false)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, llm.Message{Role: llm.User, Results: append(results, executed...)})
	}
	return nil, ErrBudget
}

func (r *run) finish(rep *Report, p alert.Payload, start time.Time) (*Report, error) {
	rep.Alert = p.Primary().Name()
	rep.Warnings = append(r.warn, rep.Warnings...)
	rep.Stats = r.stats
	rep.Stats.Duration = time.Since(start)
	if r.inv.Trace != nil {
		if err := r.inv.Trace.WriteReport(rep); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

func (r *run) snapshot() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	made := make(map[string]string, len(r.made))
	for k, v := range r.made {
		made[k] = v
	}
	return made
}

// execute runs calls concurrently and returns results in call order. Only a
// cancelled context or a trace write failure aborts the investigation; any
// other failure is reported to the model as an error result.
func (r *run) execute(ctx context.Context, calls []llm.Call, prefetch bool) ([]llm.Result, error) {
	results := make([]llm.Result, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		id := r.register(c.Name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = r.call(ctx, id, c, prefetch)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, errors.Join(errs...)
}

func (r *run) register(toolName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.stats.ToolCalls++
	id := fmt.Sprintf("c%d", r.next)
	r.made[id] = toolName
	return id
}

func (r *run) call(ctx context.Context, id string, c llm.Call, prefetch bool) (llm.Result, error) {
	started := time.Now()
	var res tool.Result
	var callErr error
	if !json.Valid(c.Args) {
		res = tool.Result{Text: "arguments are not valid JSON", IsError: true}
	} else {
		cctx, cancel := context.WithTimeout(ctx, r.budget.ToolTimeout)
		res, callErr = r.inv.Tools.Call(cctx, c.Name, c.Args)
		cancel()
	}

	if r.inv.Trace != nil {
		rec := trace.Call{ID: id, Tool: c.Name, Args: c.Args, Prefetch: prefetch, Result: res, StartedAt: started.UTC(), DurationMS: time.Since(started).Milliseconds()}
		if callErr != nil {
			rec.Error = callErr.Error()
		}
		if err := r.inv.Trace.WriteCall(rec); err != nil {
			return llm.Result{}, err
		}
	}
	if callErr != nil {
		res = tool.Result{Text: "tool call failed: " + callErr.Error(), IsError: true}
	}
	return llm.Result{
		CallID:  c.ID,
		Text:    fmt.Sprintf("[call %s]\n%s", id, clip(res.Text, r.budget.MaxResultBytes)),
		IsError: res.IsError,
	}, nil
}

// clip shortens s to at most n bytes on a rune boundary and says so.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated: showing %d of %d bytes; narrow the query to see more]", s[:cut], cut, len(s))
}

// appendText adds text to the last user message, or a new one.
func appendText(msgs []llm.Message, text string) []llm.Message {
	last := &msgs[len(msgs)-1]
	if last.Role == llm.User {
		last.Text = strings.TrimSpace(last.Text + "\n\n" + text)
		return msgs
	}
	return append(msgs, llm.Message{Role: llm.User, Text: text})
}

func withDefaults(b Budget) Budget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultBudget.MaxSteps
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = DefaultBudget.MaxTokens
	}
	if b.ToolTimeout <= 0 {
		b.ToolTimeout = DefaultBudget.ToolTimeout
	}
	if b.MaxResultBytes <= 0 {
		b.MaxResultBytes = DefaultBudget.MaxResultBytes
	}
	return b
}
