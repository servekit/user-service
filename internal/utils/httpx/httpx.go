// Package httpx provides a shared *http.Client and small helpers for
// outbound HTTP calls inside the service. Use httpx.Default instead of
// http.DefaultClient so every caller honors the same timeout.
package httpx

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultTimeout is applied to every request through Default.
const DefaultTimeout = 30 * time.Second

// Default is the shared *http.Client used across providers and API clients.
// It carries a timeout so a hung upstream cannot pin a goroutine forever.
var Default = &http.Client{Timeout: DefaultTimeout}

// CheckStatus returns an error when the response status code is outside the
// 2xx range. It drains and closes the body so the connection can be reused.
// The error message is "unexpected status NNN" plus the raw body excerpt
// when available, which is usually enough to diagnose upstream failures
// without leaking PII.
func CheckStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 512))
	if rerr != nil {
		return fmt.Errorf("unexpected status %d (body unreadable: %w)", resp.StatusCode, rerr)
	}
	if len(body) == 0 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}
