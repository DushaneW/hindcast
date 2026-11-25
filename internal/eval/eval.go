// Package eval replays recorded incidents against a model and checks the
// reports against expectations.
//
// A scenario is a directory holding trace.jsonl (alert, tools and recorded
// tool results) and scenario.yaml:
//
//	description: Pods OOMKilled after a deploy lowered the memory limit
//	expect:
//	  root_cause: ["memory limit", "oom"]   # regexps, case-insensitive; one must match
//	  cites: [k8s__events_list]             # tools the evidence must include
//
// Matching root causes by regexp is crude: it misses correct answers phrased
// differently and accepts wrong ones that mention the right words. It is a
// floor, not a grade.
package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/investigate"
	"github.com/hindcast/hindcast/internal/llm"
	"github.com/hindcast/hindcast/internal/trace"
)

// Scenario is one recorded incident with expectations.
type Scenario struct {
	Name        string `yaml:"-"`
	Description string `yaml:"description"`
	Expect      struct {
		RootCause []string `yaml:"root_cause"`
		Cites     []string `yaml:"cites"`
	} `yaml:"expect"`
	trace *trace.Trace
}

// Result is the outcome of one scenario.
type Result struct {
	Scenario string              `json:"scenario"`
	Pass     bool                `json:"pass"`
	Failures []string            `json:"failures,omitempty"`
	Error    string              `json:"error,omitempty"`
	Report   *investigate.Report `json:"report,omitempty"`
}

// Load reads every scenario under dir, sorted by name.
func Load(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Scenario
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := loadScenario(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("scenario %s: %w", e.Name(), err)
		}
		s.Name = e.Name()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadScenario(dir string) (*Scenario, error) {
	data, err := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if len(s.Expect.RootCause) == 0 {
		return nil, fmt.Errorf("expect.root_cause is empty")
	}
	for _, re := range s.Expect.RootCause {
		if _, err := regexp.Compile("(?i)" + re); err != nil {
			return nil, fmt.Errorf("expect.root_cause %q: %w", re, err)
		}
	}
	s.trace, err = trace.ReadFile(filepath.Join(dir, "trace.jsonl"))
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Run replays one scenario. traceOut, if not nil, receives the new trace.
func Run(ctx context.Context, s *Scenario, model llm.Model, budget investigate.Budget, traceOut *trace.Writer) Result {
	res := Result{Scenario: s.Name}
	payload, err := alert.Parse(s.trace.Header.Alert)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	box, err := trace.NewReplayer(s.trace)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	inv := &investigate.Investigator{Model: model, Tools: box, Trace: traceOut, Budget: budget}
	rep, err := inv.Run(ctx, payload)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Report = rep
	res.Failures = s.check(rep)
	res.Pass = len(res.Failures) == 0
	return res
}

func (s *Scenario) check(rep *investigate.Report) []string {
	var failures []string
	matched := false
	for _, re := range s.Expect.RootCause {
		if regexp.MustCompile("(?i)" + re).MatchString(rep.RootCause) {
			matched = true
			break
		}
	}
	if !matched {
		failures = append(failures, fmt.Sprintf("root cause %q matches none of %q", rep.RootCause, s.Expect.RootCause))
	}
	cited := make(map[string]bool)
	for _, e := range rep.Evidence {
		cited[e.Tool] = true
	}
	for _, t := range s.Expect.Cites {
		if !cited[t] {
			failures = append(failures, fmt.Sprintf("evidence does not cite %s", t))
		}
	}
	return failures
}
