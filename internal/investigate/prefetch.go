package investigate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/DushaneW/Hindcast/internal/alert"
	"github.com/DushaneW/Hindcast/internal/llm"
	"github.com/DushaneW/Hindcast/internal/trace"
)

// PrefetchCall is a tool call made for every alert before the model starts.
// String values in Args (at any depth) are Go templates evaluated against
// the primary alert, for example "{{.Labels.namespace}}" or
// `{{rfc3339 (before .StartsAt "30m")}}`.
type PrefetchCall struct {
	Tool string         `yaml:"tool"`
	Args map[string]any `yaml:"args"`
	// Literal skips templating. Replays set it, because recorded arguments
	// are final values that may legitimately contain "{{".
	Literal bool `yaml:"-"`
}

// PrefetchFromTrace returns the prefetch calls of a recorded investigation,
// so a replay starts from the same context the original model saw.
//
// Prefetch calls run concurrently, so the trace holds them in completion
// order. They are returned in ID order, which is the order they were issued
// in; that keeps call IDs, and so citations, stable across replays.
func PrefetchFromTrace(tr *trace.Trace) ([]PrefetchCall, error) {
	var calls []trace.Call
	for _, c := range tr.Calls {
		if c.Prefetch {
			calls = append(calls, c)
		}
	}
	seq := func(id string) int {
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "c"))
		return n
	}
	sort.SliceStable(calls, func(i, j int) bool { return seq(calls[i].ID) < seq(calls[j].ID) })

	var out []PrefetchCall
	for _, c := range calls {
		args := map[string]any{}
		if len(c.Args) > 0 {
			if err := json.Unmarshal(c.Args, &args); err != nil {
				return nil, fmt.Errorf("prefetch call %s: %w", c.ID, err)
			}
		}
		out = append(out, PrefetchCall{Tool: c.Tool, Args: args, Literal: true})
	}
	return out, nil
}

var templateFuncs = template.FuncMap{
	"rfc3339": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"unix":    func(t time.Time) int64 { return t.Unix() },
	"before": func(t time.Time, d string) (time.Time, error) {
		dur, err := time.ParseDuration(d)
		return t.Add(-dur), err
	},
	"after": func(t time.Time, d string) (time.Time, error) {
		dur, err := time.ParseDuration(d)
		return t.Add(dur), err
	},
}

// expandPrefetch renders the configured calls for an alert. A call whose
// template fails (for example, a label the alert does not have) is skipped
// with a warning instead of failing the investigation.
func expandPrefetch(calls []PrefetchCall, a alert.Alert) ([]llm.Call, []string) {
	var out []llm.Call
	var warnings []string
	for i, pc := range calls {
		var args any = pc.Args
		var err error
		if !pc.Literal {
			args, err = render(pc.Args, a)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("prefetch %s skipped: %v", pc.Tool, err))
			continue
		}
		data, err := json.Marshal(args)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("prefetch %s skipped: %v", pc.Tool, err))
			continue
		}
		out = append(out, llm.Call{ID: fmt.Sprintf("prefetch_%d", i+1), Name: pc.Tool, Args: data})
	}
	return out, warnings
}

func render(v any, a alert.Alert) (any, error) {
	switch v := v.(type) {
	case string:
		t, err := template.New("arg").Funcs(templateFuncs).Option("missingkey=error").Parse(v)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, a); err != nil {
			return nil, err
		}
		return buf.String(), nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			r, err := render(item, a)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			r, err := render(item, a)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}
