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
