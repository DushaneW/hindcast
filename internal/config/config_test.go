package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hindcast.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(write(t, `
model:
  provider: openai
  name: qwen3
  base_url: http://localhost:11434/v1
servers:
  k8s:
    command: [kubernetes-mcp-server, --read-only]
    allow: [pods_log]
budget:
  max_steps: 8
  tool_timeout: 10s
prefetch:
  - tool: k8s__events_list
    args: {namespace: "{{.Labels.namespace}}"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Name != "qwen3" || cfg.Servers["k8s"].Allow[0] != "pods_log" {
		t.Errorf("config = %+v", cfg)
	}
	if b := cfg.Budget.Investigate(); b.MaxSteps != 8 || b.ToolTimeout != 10*time.Second {
		t.Errorf("budget = %+v", b)
	}
	if cfg.Prefetch[0].Args["namespace"] != "{{.Labels.namespace}}" {
		t.Errorf("prefetch = %+v", cfg.Prefetch)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown field":    {"model: {provider: openai, name: x, temperature: 1}", "temperature"},
		"no provider":      {"model: {name: x}", "model.provider is required"},
		"bad provider":     {"model: {provider: gemini, name: x}", "not anthropic or openai"},
		"empty command":    {"model: {provider: openai, name: x}\nservers: {k8s: {}}", "servers.k8s.command"},
		"prefetch no tool": {"model: {provider: openai, name: x}\nprefetch: [{args: {}}]", "prefetch[0].tool"},
	} {
		_, err := Load(write(t, tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestAPIKey(t *testing.T) {
	t.Setenv("HINDCAST_TEST_KEY", "secret")
	if key, err := (Model{APIKeyEnv: "HINDCAST_TEST_KEY"}).APIKey(); err != nil || key != "secret" {
		t.Errorf("key = %q, err = %v", key, err)
	}
	if _, err := (Model{APIKeyEnv: "HINDCAST_UNSET_KEY"}).APIKey(); err == nil {
		t.Error("an unset variable should be an error")
	}
	if key, err := (Model{}).APIKey(); err != nil || key != "" {
		t.Error("no variable configured means no key and no error")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load("../../hindcast.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
