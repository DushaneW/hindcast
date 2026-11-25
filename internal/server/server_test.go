package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DushaneW/Hindcast/internal/alert"
)

const firing = `{"groupKey": "{}:{alertname=\"X\"}", "status": "firing",
  "alerts": [{"status": "firing", "labels": {"alertname": "X"}, "fingerprint": "f1"}]}`

const resolved = `{"groupKey": "g", "status": "resolved",
  "alerts": [{"status": "resolved", "labels": {"alertname": "X"}}]}`

type harness struct {
	srv     *Server
	http    *httptest.Server
	release chan struct{}
	mu      sync.Mutex
	seen    []string
	done    chan struct{}
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{release: make(chan struct{}), done: make(chan struct{}, 16)}
	cfg.Investigate = func(ctx context.Context, p alert.Payload) error {
		h.mu.Lock()
		h.seen = append(h.seen, p.Primary().Name())
		h.mu.Unlock()
		select {
		case <-h.release:
		case <-ctx.Done():
		}
		h.done <- struct{}{}
		return nil
	}
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	h.srv = New(cfg)
	h.http = httptest.NewServer(h.srv.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.srv.Run(ctx); close(stopped) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		h.http.Close()
	})
	return h
}

func (h *harness) post(t *testing.T, body, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.http.URL+"/alerts", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestQueueAndDedupe(t *testing.T) {
	h := newHarness(t, Config{Cooldown: time.Hour})

	if code, body := h.post(t, firing, ""); code != http.StatusAccepted {
		t.Fatalf("first delivery: %d %s", code, body)
	}
	if code, body := h.post(t, firing, ""); code != http.StatusOK || !strings.Contains(body, "already") {
		t.Errorf("repeat while running: %d %s", code, body)
	}
	h.release <- struct{}{}
	<-h.done
	if code, body := h.post(t, firing, ""); code != http.StatusOK || !strings.Contains(body, "already") {
		t.Errorf("repeat within cooldown: %d %s", code, body)
	}

	h.srv.mu.Lock()
	st := h.srv.state[`{}:{alertname="X"}`]
	st.finished = time.Now().Add(-2 * time.Hour)
	h.srv.state[`{}:{alertname="X"}`] = st
	h.srv.mu.Unlock()
	if code, _ := h.post(t, firing, ""); code != http.StatusAccepted {
		t.Errorf("after cooldown: %d", code)
	}
	h.release <- struct{}{}
	<-h.done

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.seen) != 2 {
		t.Errorf("investigations = %v", h.seen)
	}
}

func TestRejects(t *testing.T) {
	h := newHarness(t, Config{Token: "s3cret"})
	for name, tc := range map[string]struct {
		body, token string
		code        int
	}{
		"no token":  {firing, "", http.StatusUnauthorized},
		"bad token": {firing, "wrong", http.StatusUnauthorized},
		"not json":  {"nope", "s3cret", http.StatusBadRequest},
		"resolved":  {resolved, "s3cret", http.StatusOK},
		"too large": {strings.Repeat(" ", maxBody+1), "s3cret", http.StatusRequestEntityTooLarge},
	} {
		if code, body := h.post(t, tc.body, tc.token); code != tc.code {
			t.Errorf("%s: %d %s, want %d", name, code, body, tc.code)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.seen) != 0 {
		t.Errorf("nothing should have been investigated: %v", h.seen)
	}
}

func TestQueueFull(t *testing.T) {
	h := newHarness(t, Config{Workers: 1, Queue: 1})
	payload := func(n string) string {
		return `{"groupKey": "` + n + `", "alerts": [{"status": "firing", "labels": {"alertname": "` + n + `"}}]}`
	}
	if code, _ := h.post(t, payload("a"), ""); code != http.StatusAccepted {
		t.Fatal("a should be accepted")
	}
	// Wait until the worker has taken "a", so the queue slot is free.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		started := len(h.seen) == 1
		h.mu.Unlock()
		if started || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := h.post(t, payload("b"), ""); code != http.StatusAccepted {
		t.Fatal("b should fill the queue")
	}
	if code, _ := h.post(t, payload("c"), ""); code != http.StatusServiceUnavailable {
		t.Errorf("c: want 503, got %d", code)
	}
	// c was released, so it can be retried once there is room.
	h.release <- struct{}{}
	<-h.done
	deadline = time.Now().Add(2 * time.Second)
	for {
		code, _ := h.post(t, payload("c"), "")
		if code == http.StatusAccepted || time.Now().After(deadline) {
			if code != http.StatusAccepted {
				t.Errorf("c retry: %d", code)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(h.release)
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, Config{Token: "x"})
	resp, err := http.Get(h.http.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
}
