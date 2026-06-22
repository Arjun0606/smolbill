// Package webhook delivers smolbill lifecycle events (invoice.finalized,
// drift.detected) to registered HTTP endpoints, signed with a per-endpoint
// secret so receivers can verify authenticity. Delivery is abstracted behind a
// Sender so the API can emit events in tests without real HTTP — the same
// testability pattern as the spend-alert notifier.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Event is one delivered lifecycle event. The JSON shape is the webhook payload.
type Event struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	OccurredAt time.Time      `json:"occurred_at"`
	Data       map[string]any `json:"data"`
}

// Sign returns the hex HMAC-SHA256 of body under secret. This is the value sent
// in the X-Smolbill-Signature header; a receiver recomputes it over the raw body
// to prove the payload came from us and wasn't tampered with in transit.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Sender delivers an event to a single endpoint, signing with its secret.
type Sender interface {
	Send(ctx context.Context, url, secret string, e Event) error
}

// HTTPSender POSTs the event as signed JSON over net/http.
type HTTPSender struct{ HTTP *http.Client }

// NewHTTPSender returns a Sender with a sane delivery timeout.
func NewHTTPSender() *HTTPSender {
	return &HTTPSender{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send implements Sender.
func (h *HTTPSender) Send(ctx context.Context, url, secret string, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Smolbill-Event", e.Type)
	req.Header.Set("X-Smolbill-Signature", Sign(secret, body))
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s returned %d", url, resp.StatusCode)
	}
	return nil
}
