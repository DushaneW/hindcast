package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hindcast/hindcast/internal/tool"
)

func TestBuiltin(t *testing.T) {
	r, err := New(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ in, want string }{
		{"key AKIAIOSFODNN7EXAMPLE used", "key [REDACTED:aws-key] used"},
		{"token ghp_" + strings.Repeat("a", 36), "token [REDACTED:github-token]"},
		{"Authorization: Bearer abcdefghijklmnop1234", "Authorization: Bearer [REDACTED:auth]"},
		{"postgres://app:hunter22@db:5432/orders", "postgres://app:[REDACTED:password]@db:5432/orders"},
		{`password: "s3cr3t-value"`, `password: "[REDACTED:credential]"`},
		{"DB_PASSWORD=supersecret1", "DB_PASSWORD=[REDACTED:credential]"},
		{`"stripe_api_key": "abcdef123456"`, `"stripe_api_key": "[REDACTED:credential]"`},
		{"- name: DB_PASSWORD\n  value: hunter22", "- name: DB_PASSWORD\n  value: [REDACTED:credential]"},
		{"api_key=abcdef123456 next", "api_key=[REDACTED:credential] next"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\nabc\n-----END RSA PRIVATE KEY-----", "[REDACTED:private-key]"},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "[REDACTED:jwt]"},
	} {
		if got := r.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", tc.in, got, tc.want)
		}
	}
}

func TestLeavesOrdinaryTextAlone(t *testing.T) {
	r, _ := New(true, nil)
	for _, s := range []string{
		"secretKeyRef: {name: stripe, key: webhook-secret}",
		"Container checkout-api exceeded memory limit",
		"token from the continue field",
		// Found on a live cluster: a YAML key ending in "token" whose value is a nested map.
		"- serviceAccountToken:\n    expirationSeconds: 3607",
	} {
		if got := r.Redact(s); got != s {
			t.Errorf("changed %q to %q", s, got)
		}
	}
	if r.Count() != 0 {
		t.Errorf("count = %d", r.Count())
	}
}

func TestExtraPatterns(t *testing.T) {
	r, err := New(false, []string{`cust-\d{6}`})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Redact("order for cust-123456 and cust-654321, key AKIAIOSFODNN7EXAMPLE"); got != "order for [REDACTED] and [REDACTED], key AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("got %q", got)
	}
	if r.Count() != 2 {
		t.Errorf("count = %d", r.Count())
	}
	if _, err := New(false, []string{"("}); err == nil {
		t.Error("invalid pattern should be rejected")
	}
}

type staticBox struct{ text string }

func (b staticBox) Tools() []tool.Spec { return nil }
func (b staticBox) Call(context.Context, string, json.RawMessage) (tool.Result, error) {
	return tool.Result{Text: b.text}, nil
}

func TestBox(t *testing.T) {
	r, _ := New(true, nil)
	res, err := Box{Box: staticBox{"password=hunter2222"}, Redactor: r}.Call(context.Background(), "x", nil)
	if err != nil || res.Text != "password=[REDACTED:credential]" {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}
