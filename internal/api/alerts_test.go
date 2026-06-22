package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/Arjun0606/smolbill/internal/alerts"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// recordingNotifier captures fired alerts instead of calling a real webhook.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []alerts.Notification
}

func (r *recordingNotifier) Notify(_ context.Context, _ string, n alerts.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
	return nil
}

func (r *recordingNotifier) thresholds() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for _, n := range r.sent {
		out = append(out, n.Threshold)
	}
	return out
}

func newHandleWithNotifier(t *testing.T) (*serverHandle, *recordingNotifier) {
	t.Helper()
	st := memory.New()
	srv := New(st, ingest.New(st, 0), fixedClock())
	rec := &recordingNotifier{}
	srv.SetNotifier(rec)
	return newHandleFrom(t, srv), rec
}

// setupSubForAlerts creates a customer + per-unit plan ($0.01/token) + active sub.
func setupSubForAlerts(t *testing.T, ts *serverHandle) (custID string) {
	t.Helper()
	_, cust := ts.post(t, "/v1/customers", map[string]any{"name": "Acme"})
	custID = cust["id"].(string)
	ts.post(t, "/v1/meters", map[string]any{"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n"})
	_, plan := ts.post(t, "/v1/plans", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.01"},
		},
	})
	ts.post(t, "/v1/subscriptions", map[string]any{
		"customer_id": custID, "plan_id": plan["id"].(string),
		"period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
		"started_at": "2026-06-01T00:00:00Z",
	})
	return custID
}

func ingestTokens(t *testing.T, ts *serverHandle, custID, key string, n int) {
	t.Helper()
	resp, _ := ts.post(t, "/v1/events", map[string]any{
		"idempotency_key": key, "customer_id": custID, "meter_code": "tokens",
		"event_time": "2026-06-10T00:00:00Z", "properties": map[string]any{"n": n},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest %s status %d", key, resp.StatusCode)
	}
}

func TestSpendAlertFiresAtThresholds(t *testing.T) {
	ts, rec := newHandleWithNotifier(t)
	custID := setupSubForAlerts(t, ts)

	// Budget $100 (= 10000 tokens at $0.01). Default thresholds 50/80/100.
	resp, _ := ts.post(t, "/v1/alerts", map[string]any{
		"customer_id": custID, "budget": "100.00", "currency": "USD", "webhook_url": "https://hook.test/x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create alert status %d", resp.StatusCode)
	}

	ingestTokens(t, ts, custID, "e1", 4000) // $40 -> 40% -> nothing
	if len(rec.thresholds()) != 0 {
		t.Fatalf("fired %v at 40%%, want none", rec.thresholds())
	}

	ingestTokens(t, ts, custID, "e2", 2000) // total $60 -> 60% -> fire 50
	if got := rec.thresholds(); len(got) != 1 || got[0] != 50 {
		t.Fatalf("after 60%%, fired %v, want [50]", got)
	}

	ingestTokens(t, ts, custID, "e3", 3000) // total $90 -> 90% -> fire 80
	if got := rec.thresholds(); len(got) != 2 || got[1] != 80 {
		t.Fatalf("after 90%%, fired %v, want [50 80]", got)
	}

	ingestTokens(t, ts, custID, "e4", 2000) // total $110 -> 110% -> fire 100
	if got := rec.thresholds(); len(got) != 3 || got[2] != 100 {
		t.Fatalf("after 110%%, fired %v, want [50 80 100]", got)
	}

	// No re-firing: another event past 100% must not fire again.
	ingestTokens(t, ts, custID, "e5", 1000)
	if got := rec.thresholds(); len(got) != 3 {
		t.Fatalf("re-fired beyond 100%%: %v", got)
	}
}

func TestSpendAlertPayload(t *testing.T) {
	ts, rec := newHandleWithNotifier(t)
	custID := setupSubForAlerts(t, ts)
	ts.post(t, "/v1/alerts", map[string]any{
		"customer_id": custID, "budget": "100.00", "currency": "USD", "webhook_url": "https://hook.test/x",
	})
	ingestTokens(t, ts, custID, "e1", 6000) // $60 -> 60%

	if len(rec.sent) != 1 {
		t.Fatalf("notifications = %d, want 1", len(rec.sent))
	}
	n := rec.sent[0]
	if n.Threshold != 50 || n.Spent != "60.00" || n.Budget != "100.00" || n.PctUsed != "60.0" {
		t.Fatalf("payload wrong: %+v", n)
	}
}
