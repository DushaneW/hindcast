package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/config"
	"github.com/hindcast/hindcast/internal/investigate"
	"github.com/hindcast/hindcast/internal/llm"
	"github.com/hindcast/hindcast/internal/mcpbox"
	"github.com/hindcast/hindcast/internal/notify"
	"github.com/hindcast/hindcast/internal/redact"
	"github.com/hindcast/hindcast/internal/trace"
)

// live is what investigations against real systems share: the model, the
// MCP sessions and the notifier. It is safe for concurrent investigations.
type live struct {
	cfg      *config.Config
	model    llm.Model
	box      *mcpbox.Box
	slack    *notify.Slack
	traceDir string
	log      io.Writer
}

func openLive(ctx context.Context, cfg *config.Config, traceDir string, serverLog, log io.Writer) (*live, error) {
	if _, err := redact.New(!cfg.Redact.DisableBuiltin, cfg.Redact.Patterns); err != nil {
		return nil, err
	}
	model, err := newModel(cfg.Model)
	if err != nil {
		return nil, err
	}
	l := &live{cfg: cfg, model: model, traceDir: traceDir, log: log}
	if s := cfg.Notify.Slack; s != nil {
		url, err := s.WebhookURL()
		if err != nil {
			return nil, err
		}
		l.slack = &notify.Slack{WebhookURL: url}
	}
	l.box, err = connect(ctx, cfg, serverLog)
	if err != nil {
		return nil, err
	}
	if hidden := l.box.Hidden(); len(hidden) > 0 {
		fmt.Fprintf(log, "read-only policy hid %d tool(s); see \"hindcast tools\"\n", len(hidden))
	}
	return l, nil
}

func (l *live) Close() error { return l.box.Close() }

// investigate runs one investigation. It records the trace and writes the
// report next to it (when traceDir is set), then notifies. A notification
// failure is returned together with the report.
func (l *live) investigate(ctx context.Context, p alert.Payload) (*investigate.Report, error) {
	// A redactor per investigation, so its count belongs to this one.
	redactor, err := redact.New(!l.cfg.Redact.DisableBuiltin, l.cfg.Redact.Patterns)
	if err != nil {
		return nil, err
	}
	inv := &investigate.Investigator{
		Model:    l.model,
		Tools:    redact.Box{Box: l.box, Redactor: redactor},
		Budget:   l.cfg.Budget.Investigate(),
		Prefetch: l.cfg.Prefetch,
	}

	var tracePath string
	if l.traceDir != "" {
		f, path, err := createTrace(l.traceDir, p.Primary().Name())
		if err != nil {
			return nil, err
		}
		defer f.Close()
		inv.Trace = trace.NewWriter(f)
		tracePath = path
		fmt.Fprintf(l.log, "recording to %s\n", path)
	}

	rep, err := inv.Run(ctx, p)
	if n := redactor.Count(); n > 0 {
		fmt.Fprintf(l.log, "redacted %d value(s) in tool output\n", n)
	}
	if err != nil {
		return nil, err
	}

	if tracePath != "" {
		mdPath := strings.TrimSuffix(tracePath, ".jsonl") + ".md"
		if err := os.WriteFile(mdPath, []byte(rep.Markdown()), 0o600); err != nil {
			return rep, fmt.Errorf("write report: %w", err)
		}
	}
	if l.slack != nil {
		if err := l.slack.Send(ctx, rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

var unsafeFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// createTrace creates a new trace file named after the time and the alert,
// adding a counter when two investigations start in the same second.
func createTrace(dir, name string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	base := time.Now().UTC().Format("20060102T150405Z") + "-" + unsafeFileChars.ReplaceAllString(name, "_")
	for n := 0; n < 100; n++ {
		path := filepath.Join(dir, base+".jsonl")
		if n > 0 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d.jsonl", base, n))
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("too many traces named %s in %s", base, dir)
}
