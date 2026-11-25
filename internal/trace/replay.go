package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/DushaneW/Hindcast/internal/tool"
)

// Replayer is a tool.Box that answers from a recorded trace. A call matches a
// recording when the tool name and the arguments (compared as canonical JSON)
// are equal. Unmatched calls get an error result listing the argument sets
// that were recorded for that tool, so a model can still find the data.
type Replayer struct {
	tools    []tool.Spec
	recorded map[string]tool.Result
	argsSeen map[string][]string
}

// NewReplayer builds a Replayer from a trace.
func NewReplayer(tr *Trace) (*Replayer, error) {
	r := &Replayer{
		tools:    tr.Header.Tools,
		recorded: make(map[string]tool.Result),
		argsSeen: make(map[string][]string),
	}
	for _, c := range tr.Calls {
		args, err := Canonical(c.Args)
		if err != nil {
			return nil, fmt.Errorf("call %s: %w", c.ID, err)
		}
		key := c.Tool + "\x00" + args
		if _, dup := r.recorded[key]; dup {
			continue
		}
		res := c.Result
		if c.Error != "" {
			res = tool.Result{Text: "tool call failed: " + c.Error, IsError: true}
		}
		r.recorded[key] = res
		r.argsSeen[c.Tool] = append(r.argsSeen[c.Tool], args)
	}
	for _, seen := range r.argsSeen {
		sort.Strings(seen)
	}
	return r, nil
}

// Tools returns the tool specs offered during the recorded investigation.
func (r *Replayer) Tools() []tool.Spec { return r.tools }

// Call returns the recorded result for the call.
func (r *Replayer) Call(ctx context.Context, name string, args json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	canon, err := Canonical(args)
	if err != nil {
		return tool.Result{Text: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if res, ok := r.recorded[name+"\x00"+canon]; ok {
		return res, nil
	}
	seen := r.argsSeen[name]
	if len(seen) == 0 {
		return tool.Result{Text: fmt.Sprintf("replay: %s was not called in the recorded investigation", name), IsError: true}, nil
	}
	return tool.Result{
		Text: fmt.Sprintf("replay: no recording of %s with these arguments. Recorded argument sets:\n%s",
			name, strings.Join(seen, "\n")),
		IsError: true,
	}, nil
}

// Canonical re-encodes JSON with sorted object keys and no insignificant
// whitespace. Empty input is treated as an empty object.
func Canonical(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}", nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	if dec.More() {
		return "", fmt.Errorf("trailing data after JSON value")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
