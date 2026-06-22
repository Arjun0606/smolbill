// Package alerts implements proactive spend alerts (build plan §9): fire a
// webhook when a customer's projected current-period spend crosses 50/80/100%
// of their budget — before the overage, never after. This is a direct
// expression of the "surface every state change to the user" principle: silence
// while a customer sails toward a surprise bill is a bug.
//
// The threshold math is pure and deterministic; delivery is via a Notifier so it
// is testable without real webhooks.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// DefaultThresholds are the percentages alerts fire at when none are specified.
var DefaultThresholds = []int{50, 80, 100}

// Crossed returns the thresholds that should fire now and the new high-water
// mark, given the previously-fired maximum and the current usage percentage.
//
// Alerts are monotonic per period: a threshold fires at most once (we only fire
// thresholds strictly above maxFired that the current pct has reached). This
// prevents the alert spam that would train users to ignore them.
func Crossed(maxFired int, pct decimal.Decimal, thresholds []int) (toFire []int, newMax int) {
	newMax = maxFired
	sorted := append([]int(nil), thresholds...)
	sort.Ints(sorted)
	pctFloat := pct // compared via decimal to avoid float drift
	for _, t := range sorted {
		if t > maxFired && pctFloat.GreaterThanOrEqual(decimal.NewFromInt(int64(t))) {
			toFire = append(toFire, t)
			if t > newMax {
				newMax = t
			}
		}
	}
	return toFire, newMax
}

// Notification is the payload delivered when a threshold is crossed.
type Notification struct {
	CustomerID string `json:"customer_id"`
	Threshold  int    `json:"threshold_pct"`
	Budget     string `json:"budget"`
	Spent      string `json:"projected_spent"`
	PctUsed    string `json:"pct_used"`
	Currency   string `json:"currency"`
	FiredAt    string `json:"fired_at"`
}

// Notifier delivers a Notification to a destination (e.g. a webhook URL).
type Notifier interface {
	Notify(ctx context.Context, url string, n Notification) error
}

// WebhookNotifier POSTs the notification as JSON to the configured URL.
type WebhookNotifier struct {
	HTTP *http.Client
}

// NewWebhookNotifier returns a Notifier with a sane timeout.
func NewWebhookNotifier() *WebhookNotifier {
	return &WebhookNotifier{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Notify implements Notifier.
func (w *WebhookNotifier) Notify(ctx context.Context, url string, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alerts: webhook %s returned %d", url, resp.StatusCode)
	}
	return nil
}
