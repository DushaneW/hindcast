package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DushaneW/Hindcast/internal/investigate"
)

var report = &investigate.Report{
	Alert:      "KubePodCrashLooping",
	RootCause:  "memory limit cut to 256Mi in <5d0f7b4>",
	Confidence: investigate.Medium,
	Summary:    "Pods are OOMKilled & restarting.",
	Evidence:   []investigate.Evidence{{CallID: "c1", Tool: "k8s__events_list", Finding: "OOMKilled events"}},
	NextSteps:  []string{"Revert 5d0f7b4"},
	Warnings:   []string{"dropped evidence citing unknown call"},
}

func TestSlackText(t *testing.T) {
	text := SlackText(report)
	for _, want := range []string{
		"*KubePodCrashLooping*: memory limit cut to 256Mi in &lt;5d0f7b4&gt; _(medium confidence)_",
		"OOMKilled &amp; restarting",
		"• `c1` k8s__events_list: OOMKilled events",
		"• Revert 5d0f7b4",
		"1 warning(s)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	long := *report
	long.Summary = strings.Repeat("é", 4000)
	if got := SlackText(&long); len(got) > slackLimit+100 || !strings.HasSuffix(got, "see the full report.") {
		t.Errorf("long report not truncated: %d bytes", len(got))
	}
}

func TestSend(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	if err := (&Slack{WebhookURL: srv.URL}).Send(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got["text"], "*KubePodCrashLooping*") {
		t.Errorf("payload = %v", got)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_token", http.StatusForbidden)
	}))
	defer failing.Close()
	err := (&Slack{WebhookURL: failing.URL}).Send(context.Background(), report)
	if err == nil || !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("err = %v", err)
	}
}
