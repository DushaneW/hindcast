// Package config loads hindcast.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hindcast/hindcast/internal/investigate"
)

// Config is the file format.
type Config struct {
	Model    Model                      `yaml:"model"`
	Servers  map[string]Server          `yaml:"servers"`
	Budget   Budget                     `yaml:"budget"`
	Prefetch []investigate.PrefetchCall `yaml:"prefetch"`
	Redact   Redact                     `yaml:"redact"`
	Notify   Notify                     `yaml:"notify"`
	// Judge grades eval runs against scenario references. Optional; use a
	// different model from the one being evaluated where you can.
	Judge *Model `yaml:"judge"`
}

// Redact configures masking of tool output. The built-in rules are on
// unless disabled.
type Redact struct {
	DisableBuiltin bool     `yaml:"disable_builtin"`
	Patterns       []string `yaml:"patterns"`
}

// Notify lists where reports are sent besides stdout.
type Notify struct {
	Slack *Slack `yaml:"slack"`
}

// Slack is an incoming webhook. The URL is a credential, so the file names
// the environment variable that holds it.
type Slack struct {
	WebhookURLEnv string `yaml:"webhook_url_env"`
}

// WebhookURL reads the URL from the environment.
func (s Slack) WebhookURL() (string, error) {
	if s.WebhookURLEnv == "" {
		return "", errors.New("notify.slack.webhook_url_env is required")
	}
	url := os.Getenv(s.WebhookURLEnv)
	if url == "" {
		return "", fmt.Errorf("environment variable %s is not set", s.WebhookURLEnv)
	}
	return url, nil
}

// Model selects the LLM.
type Model struct {
	Provider string `yaml:"provider"` // anthropic or openai
	Name     string `yaml:"name"`
	BaseURL  string `yaml:"base_url"`
	// APIKeyEnv names the environment variable holding the key. The key
	// itself never goes in the file.
	APIKeyEnv string `yaml:"api_key_env"`
}

// Server is an MCP server started as a subprocess speaking stdio.
type Server struct {
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	Allow   []string          `yaml:"allow"`
}

// Budget mirrors investigate.Budget; zero values take the defaults.
type Budget struct {
	MaxSteps       int           `yaml:"max_steps"`
	MaxTokens      int           `yaml:"max_tokens"`
	ToolTimeout    time.Duration `yaml:"tool_timeout"`
	MaxResultBytes int           `yaml:"max_result_bytes"`
}

// Investigate converts to the investigator's budget.
func (b Budget) Investigate() investigate.Budget {
	return investigate.Budget(b)
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	errs := c.Model.validate("model")
	if c.Judge != nil {
		errs = append(errs, c.Judge.validate("judge")...)
	}
	if c.Notify.Slack != nil && c.Notify.Slack.WebhookURLEnv == "" {
		errs = append(errs, errors.New("notify.slack.webhook_url_env is required"))
	}
	for name, s := range c.Servers {
		if len(s.Command) == 0 {
			errs = append(errs, fmt.Errorf("servers.%s.command is empty", name))
		}
	}
	for i, p := range c.Prefetch {
		if p.Tool == "" {
			errs = append(errs, fmt.Errorf("prefetch[%d].tool is empty", i))
		}
	}
	return errors.Join(errs...)
}

func (m Model) validate(field string) []error {
	var errs []error
	switch m.Provider {
	case "anthropic", "openai":
	case "":
		errs = append(errs, fmt.Errorf("%s.provider is required", field))
	default:
		errs = append(errs, fmt.Errorf("%s.provider %q is not anthropic or openai", field, m.Provider))
	}
	if m.Name == "" {
		errs = append(errs, fmt.Errorf("%s.name is required", field))
	}
	return errs
}

// APIKey reads the key from the configured environment variable. An unset
// variable is an error only when one was configured.
func (m Model) APIKey() (string, error) {
	if m.APIKeyEnv == "" {
		return "", nil
	}
	key := os.Getenv(m.APIKeyEnv)
	if key == "" {
		return "", fmt.Errorf("environment variable %s is not set", m.APIKeyEnv)
	}
	return key, nil
}
