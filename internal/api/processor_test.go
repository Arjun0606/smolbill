package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments/fake"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// newHandleWithProcessor wires the API over memory with a fake payment rail.
func newHandleWithProcessor(t *testing.T) (*serverHandle, *fake.Processor) {
	t.Helper()
	st := memory.New()
	clock := fixedClock()
	srv := New(st, ingest.New(st, 0), clock)
	proc := fake.New()
	srv.SetProcessor(proc)
	return newHandleFrom(t, srv), proc
}

func TestFinalizePushesToProcessor(t *testing.T) {
	ts, proc := newHandleWithProcessor(t)
	_, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000}) // $3.00

	resp, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("finalize status %d", resp.StatusCode)
	}
	if fin["processor"] != "fake" {
		t.Fatalf("processor = %v, want fake", fin["processor"])
	}
	if fin["external_invoice_id"] == nil || fin["external_invoice_id"] == "" {
		t.Fatal("expected external_invoice_id from processor push")
	}
	if fin["status"] != "open" {
		t.Fatalf("status = %v, want open (from processor)", fin["status"])
	}
	if proc.Count() != 1 {
		t.Fatalf("processor pushes = %d, want 1", proc.Count())
	}
}

func TestFinalizeFailsWhenProcessorFails(t *testing.T) {
	ts, proc := newHandleWithProcessor(t)
	proc.FailWith = errors.New("card declined")
	_, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000})

	resp, _ := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when processor fails", resp.StatusCode)
	}
	// And the invoice must NOT be persisted (no half-commit).
	resp, _ = ts.get(t, "/v1/reconcile/inv_anything")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected no persisted invoice; reconcile status %d", resp.StatusCode)
	}
}

func TestVerifyMatchesProcessor(t *testing.T) {
	ts, _ := newHandleWithProcessor(t)
	_, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000}) // $3.00
	_, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	invID, _ := fin["invoice_id"].(string)

	// The processor billed exactly what smolbill computed -> consistent (200).
	resp, body := ts.get(t, "/v1/invoices/"+invID+"/verify")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, want 200 (consistent)", resp.StatusCode)
	}
	if body["consistent"] != true {
		t.Fatalf("consistent = %v, want true", body["consistent"])
	}
}

// TestVerifyCatchesProcessorDrift is the cross-boundary proof: if the processor
// bills something other than the ledger (a tax line, a manual edit), /verify
// catches it. No internal-only reconciler can see this.
func TestVerifyCatchesProcessorDrift(t *testing.T) {
	ts, proc := newHandleWithProcessor(t)
	_, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000}) // $3.00 = 300 minor
	_, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	invID, _ := fin["invoice_id"].(string)
	extID, _ := fin["external_invoice_id"].(string)

	// Simulate the processor billing $5.00 (500 minor) instead of $3.00.
	proc.Tamper(extID, 500)

	resp, body := ts.get(t, "/v1/invoices/"+invID+"/verify")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("verify status = %d, want 409 (drift)", resp.StatusCode)
	}
	if body["consistent"] != false {
		t.Fatalf("consistent = %v, want false", body["consistent"])
	}
	if got := body["drift_minor"]; got != float64(200) { // 500 - 300
		t.Fatalf("drift_minor = %v, want 200", got)
	}
}
