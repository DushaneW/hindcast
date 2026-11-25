package alert

import (
	"strings"
	"testing"
)

const webhook = `{
  "version": "4",
  "status": "firing",
  "receiver": "hindcast",
  "groupLabels": {"alertname": "KubePodCrashLooping"},
  "alerts": [
    {"status": "resolved", "labels": {"alertname": "KubePodCrashLooping", "pod": "api-1"}, "startsAt": "2026-11-20T02:00:00Z", "fingerprint": "a"},
    {"status": "firing", "labels": {"alertname": "KubePodCrashLooping", "pod": "api-2", "namespace": "shop"},
     "annotations": {"summary": "Pod is crash looping"}, "startsAt": "2026-11-20T03:00:00Z", "fingerprint": "b"}
  ]
}`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(webhook))
	if err != nil {
		t.Fatal(err)
	}
	primary := p.Primary()
	if primary.Labels["pod"] != "api-2" {
		t.Errorf("primary should be the first firing alert, got %v", primary.Labels)
	}
	text := p.Describe()
	for _, want := range []string{
		"Alert: KubePodCrashLooping (firing)",
		"Started: 2026-11-20T03:00:00Z",
		"  namespace: shop",
		"  summary: Pod is crash looping",
		"1 more alert(s)",
		"- KubePodCrashLooping (resolved) alertname=KubePodCrashLooping pod=api-1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("description lacks %q:\n%s", want, text)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, input := range []string{`{"alerts": []}`, `not json`} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("%s: expected an error", input)
		}
	}
}
