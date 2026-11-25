// Package redact masks credentials in tool output before the model or the
// trace sees it.
//
// Pattern matching catches credentials with a recognisable shape: private
// keys, cloud and SaaS tokens, JWTs, and "password: ..." style assignments.
// It does not catch arbitrary values, such as the base64 data of a
// Kubernetes Secret under a key with a neutral name. Do not give Hindcast
// credentials that can read secrets it does not need.
package redact

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync/atomic"

	"github.com/hindcast/hindcast/internal/tool"
)

type rule struct {
	re   *regexp.Regexp
	repl string
}

// builtin rules, most specific first so a generic rule does not eat the
// prefix a specific one needs.
var builtin = []rule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED:private-key]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED:aws-key]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{22,}`), "[REDACTED:github-token]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED:slack-token]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED:api-key]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "[REDACTED:jwt]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{16,}`), "$1 [REDACTED:auth]"},
	{regexp.MustCompile(`(?i)(://[^\s:/@]+:)[^\s@/]+@`), "${1}[REDACTED:password]@"},
	// KEY=value, key: value, "key": "value" where the key ends in a credential word.
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?key|token))(["']?[ \t]*[:=][ \t]*["']?)([^\s"',;}]{6,})`), "${1}${2}[REDACTED:credential]"},
	// Kubernetes env entries: "- name: DB_PASSWORD" followed by "value: ...".
	{regexp.MustCompile(`(?i)(name:\s*["']?[A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key)["']?\s*\n\s*value:\s*["']?)[^\s"']+`), "${1}[REDACTED:credential]"},
}

// Redactor applies the built-in rules and any extra patterns.
type Redactor struct {
	rules []rule
	count atomic.Int64
}

// New returns a Redactor. Extra patterns are Go regexps; every match is
// replaced. With builtin false only the extra patterns apply.
func New(builtinRules bool, extra []string) (*Redactor, error) {
	r := &Redactor{}
	if builtinRules {
		r.rules = append(r.rules, builtin...)
	}
	for _, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redact pattern %q: %w", p, err)
		}
		r.rules = append(r.rules, rule{re, "[REDACTED]"})
	}
	return r, nil
}

// Redact returns s with every match replaced.
func (r *Redactor) Redact(s string) string {
	for _, rl := range r.rules {
		n := len(rl.re.FindAllStringIndex(s, -1))
		if n == 0 {
			continue
		}
		r.count.Add(int64(n))
		s = rl.re.ReplaceAllString(s, rl.repl)
	}
	return s
}

// Count is the number of values redacted so far.
func (r *Redactor) Count() int { return int(r.count.Load()) }

// Box redacts the results of the box it wraps.
type Box struct {
	tool.Box
	Redactor *Redactor
}

// Call implements tool.Box.
func (b Box) Call(ctx context.Context, name string, args json.RawMessage) (tool.Result, error) {
	res, err := b.Box.Call(ctx, name, args)
	res.Text = b.Redactor.Redact(res.Text)
	return res, err
}
