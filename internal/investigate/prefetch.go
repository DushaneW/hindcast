package investigate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
	"time"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/llm"
)

// PrefetchCall is a tool call made for every alert before the model starts.
// String values in Args (at any depth) are Go templates evaluated against
// the primary alert, for example "{{.Labels.namespace}}" or
// `{{rfc3339 (before .StartsAt "30m")}}`.
type PrefetchCall struct {
	Tool string         `yaml:"tool"`
	Args map[string]any `yaml:"args"`
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
		args, err := render(pc.Args, a)
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
