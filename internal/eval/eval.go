// Package eval replays recorded incidents against a model and checks the
// reports against expectations.
//
// A scenario is a directory holding trace.jsonl (alert, tools and recorded
// tool results) and scenario.yaml:
//
//	description: Pods OOMKilled after a deploy lowered the memory limit
//	expect:
//	  reference: A commit cut the memory limit from 512Mi to 256Mi.
//	  root_cause: ["memory limit", "oom"]   # regexps, case-insensitive; one must match
//	  cites: [k8s__events_list]             # tools the evidence must include
//
// With a Judge, the root cause is graded against the reference by a model.
// Without one, it is matched against the regexps. Regexps are crude: they
// miss correct answers phrased differently and accept wrong ones that use the
// right words. They are a floor, not a grade.
package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/DushaneW/Hindcast/internal/alert"
	"github.com/DushaneW/Hindcast/internal/investigate"
	"github.com/DushaneW/Hindcast/internal/llm"
	"github.com/DushaneW/Hindcast/internal/trace"
)

// Scenario is one recorded incident with expectations.
type Scenario struct {
	Name        string `yaml:"-"`
	Description string `yaml:"description"`
	Expect      struct {
		Reference string   `yaml:"reference"`
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
	Verdict  *Verdict            `json:"verdict,omitempty"`
	Report   *investigate.Report `json:"report,omitempty"`
}

// Runner replays scenarios. Judge is optional.
type Runner struct {
	Model  llm.Model
	Judge  *Judge
	Budget investigate.Budget
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
	if len(s.Expect.RootCause) == 0 && s.Expect.Reference == "" {
		return nil, fmt.Errorf("expect needs root_cause, reference, or both")
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
func (r Runner) Run(ctx context.Context, s *Scenario, traceOut *trace.Writer) Result {
	res := Result{Scenario: s.Name}
	useJudge := r.Judge != nil && s.Expect.Reference != ""
	if !useJudge && len(s.Expect.RootCause) == 0 {
		res.Error = "scenario has only a reference; run it with a judge"
		return res
	}
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
	prefetch, err := investigate.PrefetchFromTrace(s.trace)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	inv := &investigate.Investigator{Model: r.Model, Tools: box, Trace: traceOut, Budget: r.Budget, Prefetch: prefetch}
	rep, err := inv.Run(ctx, payload)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Report = rep
	if useJudge {
		v, err := r.Judge.Grade(ctx, s.Expect.Reference, rep)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Verdict = &v
		if !v.Correct {
			res.Failures = append(res.Failures, "judge: "+v.Reason)
		}
	} else {
		res.Failures = append(res.Failures, s.matchRootCause(rep)...)
	}
	res.Failures = append(res.Failures, s.checkCitations(rep)...)
	res.Pass = len(res.Failures) == 0
	return res
}

func (s *Scenario) matchRootCause(rep *investigate.Report) []string {
	matched := false
	for _, re := range s.Expect.RootCause {
		if regexp.MustCompile("(?i)" + re).MatchString(rep.RootCause) {
			matched = true
			break
		}
	}
	if !matched {
		return []string{fmt.Sprintf("root cause %q matches none of %q", rep.RootCause, s.Expect.RootCause)}
	}
	return nil
}

func (s *Scenario) checkCitations(rep *investigate.Report) []string {
	var failures []string
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
