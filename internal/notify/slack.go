// Package notify delivers reports to chat.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hindcast/hindcast/internal/investigate"
)

// Slack posts reports to a Slack incoming webhook.
type Slack struct {
	WebhookURL string
	Client     *http.Client
}

// slackLimit keeps messages under Slack's limit for a single text field.
const slackLimit = 3500

// Send posts the report.
func (s *Slack) Send(ctx context.Context, rep *investigate.Report) error {
	body, err := json.Marshal(map[string]string{"text": SlackText(rep)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// SlackText renders the report in Slack mrkdwn.
func SlackText(rep *investigate.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*: %s _(%s confidence)_\n%s\n", escape(rep.Alert), escape(rep.RootCause), rep.Confidence, escape(rep.Summary))
	if len(rep.Evidence) > 0 {
		b.WriteString("\n*Evidence*\n")
		for _, e := range rep.Evidence {
			fmt.Fprintf(&b, "• `%s` %s: %s\n", e.CallID, e.Tool, escape(e.Finding))
		}
	}
	if len(rep.NextSteps) > 0 {
		b.WriteString("\n*Next steps*\n")
		for _, s := range rep.NextSteps {
			fmt.Fprintf(&b, "• %s\n", escape(s))
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(&b, "\n_%d warning(s); see the full report._\n", len(rep.Warnings))
	}
	text := b.String()
	if len(text) > slackLimit {
		cut := slackLimit
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
		text = text[:cut] + "\n…truncated; see the full report."
	}
	return text
}

// escape applies the three escapes Slack requires in mrkdwn text.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
