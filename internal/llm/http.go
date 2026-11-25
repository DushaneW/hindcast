package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const maxAttempts = 4

// initialBackoff is a variable so tests can shorten it.
var initialBackoff = time.Second

// StatusError is a non-2xx response from a provider.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d: %s", e.Status, e.Body)
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// postJSON sends body as JSON and decodes a 2xx response into out. Rate
// limits and server errors are retried with exponential backoff, honouring
// Retry-After when the provider sends it in seconds.
func postJSON(ctx context.Context, client *http.Client, url string, header http.Header, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header = header.Clone()
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode/100 == 2 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return nil
		}
		statusErr := &StatusError{Status: resp.StatusCode, Body: truncate(string(data), 2000)}
		if !retryable(resp.StatusCode) || attempt == maxAttempts {
			return statusErr
		}
		wait := backoff
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
