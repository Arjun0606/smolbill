package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// serverHandle gives the Phase 2 tests a fluent wrapper over the shared test
// server + post/get helpers defined in api_test.go.
type serverHandle struct{ ts *httptest.Server }

func newHandle(t *testing.T) *serverHandle {
	ts, _ := testServer(t)
	return &serverHandle{ts}
}

// fixedClock is the deterministic clock used across the API tests.
func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) }
}

// newHandleFrom wraps an already-constructed Server (e.g. with a processor set).
func newHandleFrom(t *testing.T, srv *Server) *serverHandle {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &serverHandle{ts}
}

func (h *serverHandle) post(t *testing.T, path string, body any) (*http.Response, map[string]any) {
	return post(t, h.ts, path, body)
}

func (h *serverHandle) get(t *testing.T, path string) (*http.Response, map[string]any) {
	return get(t, h.ts, path)
}

// setupSubWithUsage creates customer+meter+plan+subscription and ingests the
// given token events, returning the customer and subscription IDs.
func setupSubWithUsage(t *testing.T, ts *serverHandle, tokenEvents map[string]int) (custID, subID string) {
	t.Helper()
	_, cust := ts.post(t, "/v1/customers", map[string]any{"name": "Acme"})
	custID = cust["id"].(string)
	ts.post(t, "/v1/meters", map[string]any{"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n"})
	_, plan := ts.post(t, "/v1/plans", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
		},
	})
	planID := plan["id"].(string)
	_, sub := ts.post(t, "/v1/subscriptions", map[string]any{
		"customer_id": custID, "plan_id": planID,
		"period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
		"started_at": "2026-06-01T00:00:00Z",
	})
	subID = sub["id"].(string)
	for key, n := range tokenEvents {
		ts.post(t, "/v1/events", map[string]any{
			"idempotency_key": key, "customer_id": custID, "meter_code": "tokens",
			"event_time": "2026-06-10T00:00:00Z", "properties": map[string]any{"n": n},
		})
	}
	return custID, subID
}

func TestFinalizeThenReconcileConsistent(t *testing.T) {
	ts := newHandle(t)
	_, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000}) // $3.00

	resp, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("finalize status %d", resp.StatusCode)
	}
	invID := fin["invoice_id"].(string)
	if fin["total"] != "3.00" {
		t.Fatalf("finalized total = %v, want 3.00", fin["total"])
	}

	// No new events -> reconcile must be consistent.
	resp, proof := ts.get(t, "/v1/reconcile/"+invID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reconcile status %d (want 200 consistent), body=%v", resp.StatusCode, proof)
	}
	if proof["verdict"] != "consistent" {
		t.Fatalf("verdict = %v, want consistent", proof["verdict"])
	}
	if proof["hash_match"] != true {
		t.Fatalf("hash_match = %v, want true", proof["hash_match"])
	}
}

func TestFinalizeThenLateEventDriftDetected(t *testing.T) {
	ts := newHandle(t)
	custID, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000}) // $3.00

	_, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	invID := fin["invoice_id"].(string)

	// A late event arrives AFTER finalize.
	ts.post(t, "/v1/events", map[string]any{
		"idempotency_key": "late1", "customer_id": custID, "meter_code": "tokens",
		"event_time": "2026-06-12T00:00:00Z", "properties": map[string]any{"n": 5000},
	})

	resp, proof := ts.get(t, "/v1/reconcile/"+invID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reconcile status %d, want 409 (drift)", resp.StatusCode)
	}
	if proof["verdict"] != "drift_detected" {
		t.Fatalf("verdict = %v, want drift_detected", proof["verdict"])
	}
	if proof["stored_total"] != "3.00" || proof["live_total"] != "8.00" {
		t.Fatalf("totals = %v -> %v, want 3.00 -> 8.00", proof["stored_total"], proof["live_total"])
	}
	if proof["hash_match"] != false {
		t.Fatal("hash_match should be false after drift")
	}
}

func TestEntitlementLiveUsage(t *testing.T) {
	ts := newHandle(t)
	custID, _ := setupSubWithUsage(t, ts, map[string]int{"e1": 6000})

	// Metered entitlement: 10000 token allowance for June.
	resp, _ := ts.post(t, "/v1/entitlements", map[string]any{
		"customer_id": custID, "feature": "tokens", "kind": "metered", "meter_code": "tokens",
		"limit_value": "10000", "period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create entitlement status %d", resp.StatusCode)
	}

	resp, body := ts.get(t, "/v1/entitlements/"+custID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get entitlements status %d", resp.StatusCode)
	}
	ents := body["entitlements"].([]any)
	if len(ents) != 1 {
		t.Fatalf("entitlements = %d, want 1", len(ents))
	}
	e := ents[0].(map[string]any)
	if e["used"] != "6000" || e["remaining"] != "4000" {
		t.Fatalf("used/remaining = %v/%v, want 6000/4000", e["used"], e["remaining"])
	}
	if e["within_limit"] != true || e["pct_used"] != "60.0" {
		t.Fatalf("within_limit/pct = %v/%v, want true/60.0", e["within_limit"], e["pct_used"])
	}
}

func TestEntitlementOverLimit(t *testing.T) {
	ts := newHandle(t)
	custID, _ := setupSubWithUsage(t, ts, map[string]int{"e1": 12000})
	ts.post(t, "/v1/entitlements", map[string]any{
		"customer_id": custID, "feature": "tokens", "kind": "metered", "meter_code": "tokens",
		"limit_value": "10000", "period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
	})
	_, body := ts.get(t, "/v1/entitlements/"+custID)
	e := body["entitlements"].([]any)[0].(map[string]any)
	if e["within_limit"] != false {
		t.Fatalf("within_limit = %v, want false (12000 > 10000)", e["within_limit"])
	}
}
