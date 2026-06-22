package api

import (
	"sync"
	"testing"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/payments/fake"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// testClock is a goroutine-safe movable clock: the server reads it (including from
// async webhook-emit goroutines) while tests advance it, so it must be locked.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// dunningFixture builds a server with a fake processor and a movable clock, then
// finalizes one $10 invoice (10000 tokens @ $0.001) so a `scheduled` collection
// exists. It returns the handle, processor, the invoice + external ids, and a
// pointer to the clock so tests can advance time to make retries due.
func dunningFixture(t *testing.T) (h *serverHandle, proc *fake.Processor, rec *recordingSender, invID, extID string, clock *testClock) {
	t.Helper()
	st := memory.New()
	clk := &testClock{t: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)}
	srv := New(st, ingest.New(st, 0), clk.now)
	proc = fake.New()
	srv.SetProcessor(proc)
	rec = &recordingSender{}
	srv.SetWebhookSender(rec)
	// Register a catch-all webhook so emitted dunning events are delivered to rec.
	if err := st.PutWebhook(domain.Webhook{ID: "wh", URL: "https://sink", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	h = newHandleFrom(t, srv)

	_, subID := setupSubWithUsage(t, h, map[string]int{"k1": 10000})
	resp, body := h.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	if resp.StatusCode != 201 {
		t.Fatalf("finalize status %d: %v", resp.StatusCode, body)
	}
	invID, _ = body["invoice_id"].(string)
	extID, _ = body["external_invoice_id"].(string)
	if invID == "" || extID == "" {
		t.Fatalf("finalize missing ids: %v", body)
	}
	return h, proc, rec, invID, extID, clk
}

func TestFinalizeOpensScheduledCollection(t *testing.T) {
	h, _, _, invID, extID, _ := dunningFixture(t)
	_, col := h.get(t, "/v1/invoices/"+invID+"/collection")
	if col["status"] != "scheduled" {
		t.Errorf("status = %v, want scheduled", col["status"])
	}
	if col["external_invoice_id"] != extID {
		t.Errorf("external id = %v, want %s", col["external_invoice_id"], extID)
	}
	if amt, _ := col["amount_minor"].(float64); amt != 1000 {
		t.Errorf("amount_minor = %v, want 1000 ($10.00)", col["amount_minor"])
	}
}

func TestDunningRecoversAfterSoftDecline(t *testing.T) {
	h, proc, _, invID, extID, _ := dunningFixture(t)
	// First charge soft-declines, the next succeeds.
	proc.ProgramCharges(extID,
		payments.ChargeResult{Status: "failed", FailureReason: "insufficient_funds"},
		payments.ChargeResult{Status: "paid"},
	)

	_, c1 := h.post(t, "/v1/invoices/"+invID+"/collect", nil)
	if c1["status"] != "retrying" {
		t.Fatalf("after soft decline: status = %v, want retrying", c1["status"])
	}
	if c1["last_reason"] != "insufficient_funds" {
		t.Errorf("last_reason = %v", c1["last_reason"])
	}
	if c1["next_attempt_at"] == nil {
		t.Error("a retrying collection must schedule a next attempt")
	}

	_, c2 := h.post(t, "/v1/invoices/"+invID+"/collect", nil)
	if c2["status"] != "paid" {
		t.Fatalf("after retry: status = %v, want paid", c2["status"])
	}
	if att, _ := c2["attempts"].(float64); att != 2 {
		t.Errorf("attempts = %v, want 2", c2["attempts"])
	}
}

func TestHardDeclineStopsRetryingImmediately(t *testing.T) {
	h, proc, _, invID, extID, _ := dunningFixture(t)
	// An expired card can never be fixed by retrying.
	proc.ProgramCharges(extID, payments.ChargeResult{Status: "failed", FailureReason: "expired_card"})

	_, c := h.post(t, "/v1/invoices/"+invID+"/collect", nil)
	if c["status"] != "requires_action" {
		t.Fatalf("hard decline: status = %v, want requires_action", c["status"])
	}
	if c["next_attempt_at"] != nil {
		t.Error("a hard decline must NOT schedule a blind retry")
	}
	if got := proc.ChargeCalls[extID]; got != 1 {
		t.Errorf("processor charged %d times; a hard decline must not burn retries", got)
	}
}

func TestDunningRunExhaustsToUncollectible(t *testing.T) {
	h, proc, _, invID, extID, clock := dunningFixture(t)
	// Soft-decline every attempt; the schedule has 5 retries, so attempt 6 writes
	// the invoice off.
	for i := 0; i < 8; i++ {
		proc.ProgramCharges(extID, payments.ChargeResult{Status: "failed", FailureReason: "processing_error"})
	}

	var status string
	for i := 0; i < 8; i++ {
		_, col := h.get(t, "/v1/invoices/"+invID+"/collection")
		status, _ = col["status"].(string)
		if status == "uncollectible" {
			break
		}
		// Advance the clock to when the next retry is due, then run the tick.
		if na, ok := col["next_attempt_at"].(string); ok && na != "" {
			if at, err := time.Parse(time.RFC3339, na); err == nil {
				clock.set(at)
			}
		}
		h.post(t, "/v1/dunning/run", nil)
	}
	if status != "uncollectible" {
		t.Fatalf("final status = %q, want uncollectible", status)
	}
	if got := proc.ChargeCalls[extID]; got != 6 {
		t.Errorf("charged %d times; default schedule is 1 initial + 5 retries = 6", got)
	}
}

func TestDunningRunIsIdempotentWhenNothingDue(t *testing.T) {
	h, _, _, _, _, _ := dunningFixture(t)
	// The collection is scheduled (due), so the first run attempts it (default fake
	// charge = paid). A second run finds nothing due.
	_, r1 := h.post(t, "/v1/dunning/run", nil)
	if p, _ := r1["processed"].(float64); p != 1 {
		t.Fatalf("first run processed = %v, want 1", r1["processed"])
	}
	_, r2 := h.post(t, "/v1/dunning/run", nil)
	if p, _ := r2["processed"].(float64); p != 0 {
		t.Errorf("second run processed = %v, want 0 (paid invoice is terminal)", r2["processed"])
	}
}

func TestDunningEmitsRecoveryWebhooks(t *testing.T) {
	h, proc, rec, invID, extID, _ := dunningFixture(t)
	proc.ProgramCharges(extID,
		payments.ChargeResult{Status: "failed", FailureReason: "insufficient_funds"},
		payments.ChargeResult{Status: "paid"},
	)
	h.post(t, "/v1/invoices/"+invID+"/collect", nil) // -> payment_failed
	h.post(t, "/v1/invoices/"+invID+"/collect", nil) // -> recovered

	// finalize emitted invoice.finalized; collection emitted payment_failed + recovered.
	waitForSends(t, rec, 3)
	events, _ := rec.snapshot()
	types := map[string]bool{}
	for _, e := range events {
		types[e.Type] = true
	}
	for _, want := range []string{"invoice.payment_failed", "invoice.recovered"} {
		if !types[want] {
			t.Errorf("missing webhook %s; got %v", want, types)
		}
	}
}
