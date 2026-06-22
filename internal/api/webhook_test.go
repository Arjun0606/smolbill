package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
	"github.com/Arjun0606/smolbill/internal/webhook"
)

// recordingSender captures deliveries instead of making real HTTP calls.
type recordingSender struct {
	mu   sync.Mutex
	sent []webhook.Event
	urls []string
}

func (r *recordingSender) Send(_ context.Context, url, _ string, e webhook.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, e)
	r.urls = append(r.urls, url)
	return nil
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recordingSender) snapshot() ([]webhook.Event, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]webhook.Event(nil), r.sent...), append([]string(nil), r.urls...)
}

// TestEmitFiltersBySubscription proves an event reaches exactly the endpoints
// that asked for it: an all-events hook and a type-matched hook receive it; a hook
// scoped to a different type does not.
func TestEmitFiltersBySubscription(t *testing.T) {
	st := memory.New()
	srv := New(st, ingest.New(st, 0), fixedClock())
	rec := &recordingSender{}
	srv.SetWebhookSender(rec)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutWebhook(domain.Webhook{ID: "wh_all", URL: "https://all", Secret: "s"})) // empty events = all
	must(st.PutWebhook(domain.Webhook{ID: "wh_fin", URL: "https://fin", Events: []string{"invoice.finalized"}, Secret: "s"}))
	must(st.PutWebhook(domain.Webhook{ID: "wh_drift", URL: "https://drift", Events: []string{"drift.detected"}, Secret: "s"}))

	srv.emit("invoice.finalized", map[string]any{"invoice_id": "inv_1"})

	events, urls := rec.snapshot()
	if len(events) != 2 {
		t.Fatalf("delivered to %d endpoints (%v), want 2", len(events), urls)
	}
	gotURL := map[string]bool{urls[0]: true, urls[1]: true}
	if !gotURL["https://all"] || !gotURL["https://fin"] {
		t.Errorf("delivered to %v, want https://all + https://fin", urls)
	}
	if gotURL["https://drift"] {
		t.Error("a drift-only subscriber must not receive invoice.finalized")
	}
	for _, e := range events {
		if e.Type != "invoice.finalized" || e.Data["invoice_id"] != "inv_1" {
			t.Errorf("delivered event mismatch: %+v", e)
		}
	}
}

// TestFinalizeEmitsWebhook proves the wiring end-to-end: finalizing an invoice
// over HTTP fires an invoice.finalized delivery (asynchronously) carrying the new
// invoice id.
func TestFinalizeEmitsWebhook(t *testing.T) {
	st := memory.New()
	srv := New(st, ingest.New(st, 0), fixedClock())
	rec := &recordingSender{}
	srv.SetWebhookSender(rec)
	if err := st.PutWebhook(domain.Webhook{ID: "wh_1", URL: "https://sink", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	h := newHandleFrom(t, srv)

	_, subID := setupSubWithUsage(t, h, map[string]int{"k1": 10000})
	resp, body := h.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	if resp.StatusCode != 201 {
		t.Fatalf("finalize status %d: %v", resp.StatusCode, body)
	}
	invID, _ := body["invoice_id"].(string)
	if invID == "" {
		t.Fatal("finalize did not return an invoice_id")
	}

	// Delivery is fire-and-forget (a goroutine), so poll briefly for it.
	waitForSends(t, rec, 1)
	events, _ := rec.snapshot()
	if events[0].Type != "invoice.finalized" {
		t.Errorf("event type = %q, want invoice.finalized", events[0].Type)
	}
	if events[0].Data["invoice_id"] != invID {
		t.Errorf("event invoice_id = %v, want %s", events[0].Data["invoice_id"], invID)
	}
}

func waitForSends(t *testing.T, rec *recordingSender, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d webhook delivery(ies); got %d", n, rec.count())
}
