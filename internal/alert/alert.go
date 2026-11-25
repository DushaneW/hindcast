// Package alert parses Alertmanager webhook payloads.
package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Alert is a single alert from an Alertmanager webhook.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Name returns the alertname label, or "unnamed".
func (a Alert) Name() string {
	if n := a.Labels["alertname"]; n != "" {
		return n
	}
	return "unnamed"
}

// Payload is the Alertmanager webhook body (version 4).
type Payload struct {
	Status       string            `json:"status"`
	Receiver     string            `json:"receiver"`
	GroupLabels  map[string]string `json:"groupLabels"`
	CommonLabels map[string]string `json:"commonLabels"`
	ExternalURL  string            `json:"externalURL"`
	Alerts       []Alert           `json:"alerts"`
}

// Parse decodes a webhook payload and rejects one without alerts.
func Parse(data []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return Payload{}, fmt.Errorf("decode alertmanager payload: %w", err)
	}
	if len(p.Alerts) == 0 {
		return Payload{}, errors.New("alertmanager payload has no alerts")
	}
	return p, nil
}

// Primary is the alert the investigation centres on: the first firing one,
// or the first one if none is firing.
func (p Payload) Primary() Alert {
	for _, a := range p.Alerts {
		if a.Status == "firing" {
			return a
		}
	}
	return p.Alerts[0]
}

// Describe renders the payload as plain text for the model.
func (p Payload) Describe() string {
	var b strings.Builder
	primary := p.Primary()
	fmt.Fprintf(&b, "Alert: %s (%s)\n", primary.Name(), primary.Status)
	if !primary.StartsAt.IsZero() {
		fmt.Fprintf(&b, "Started: %s\n", primary.StartsAt.UTC().Format(time.RFC3339))
	}
	writeMap(&b, "Labels", primary.Labels)
	writeMap(&b, "Annotations", primary.Annotations)
	if primary.GeneratorURL != "" {
		fmt.Fprintf(&b, "Source: %s\n", primary.GeneratorURL)
	}
	if others := len(p.Alerts) - 1; others > 0 {
		fmt.Fprintf(&b, "\n%d more alert(s) in the same group:\n", others)
		for _, a := range p.Alerts {
			if a.Fingerprint == primary.Fingerprint && a.StartsAt.Equal(primary.StartsAt) {
				continue
			}
			fmt.Fprintf(&b, "- %s (%s) %s\n", a.Name(), a.Status, inline(a.Labels))
		}
	}
	return b.String()
}

func writeMap(b *strings.Builder, title string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(b, "  %s: %s\n", k, m[k])
	}
}

func inline(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
