package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// testServer wires the API over a memory store with a fixed clock so ingest and
// proration are deterministic.
func testServer(t *testing.T) (*httptest.Server, func() time.Time) {
	t.Helper()
	st := memory.New()
	clock := func() time.Time { return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) }
	srv := New(st, ingest.New(st, 0), clock)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, clock
}

func post(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestEndToEndFlow(t *testing.T) {
	ts, _ := testServer(t)

	// Customer.
	resp, cust := post(t, ts, "/v1/customers", map[string]any{"name": "Acme"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create customer status %d", resp.StatusCode)
	}
	custID := cust["id"].(string)

	// Meter.
	resp, _ = post(t, ts, "/v1/meters", map[string]any{
		"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create meter status %d", resp.StatusCode)
	}

	// Plan: $49 flat + $0.001/token.
	resp, plan := post(t, ts, "/v1/plans", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "flat", "currency": "USD", "flat_amount": "49.00"},
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plan status %d", resp.StatusCode)
	}
	planID := plan["id"].(string)

	// Subscription, full period (no proration).
	resp, sub := post(t, ts, "/v1/subscriptions", map[string]any{
		"customer_id": custID, "plan_id": planID,
		"period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
		"started_at": "2026-06-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create subscription status %d: %v", resp.StatusCode, sub)
	}
	subID := sub["id"].(string)

	// Ingest 15000 tokens across two events.
	for i, n := range []int{10000, 5000} {
		resp, _ = post(t, ts, "/v1/events", map[string]any{
			"idempotency_key": "k" + string(rune('a'+i)), "customer_id": custID,
			"meter_code": "tokens", "event_time": "2026-06-10T00:00:00Z",
			"properties": map[string]any{"n": n},
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest status %d", resp.StatusCode)
		}
	}

	// Duplicate key -> idempotent no-op (200, status "duplicate").
	resp, dup := post(t, ts, "/v1/events", map[string]any{
		"idempotency_key": "ka", "customer_id": custID,
		"meter_code": "tokens", "event_time": "2026-06-10T00:00:00Z",
		"properties": map[string]any{"n": 99999},
	})
	if resp.StatusCode != http.StatusOK || dup["status"] != "duplicate" {
		t.Fatalf("duplicate handling wrong: status %d body %v", resp.StatusCode, dup)
	}

	// Invoice preview: 49 + 15000*0.001 = 64.00.
	resp, inv := post(t, ts, "/v1/invoices/preview", map[string]any{"subscription_id": subID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status %d", resp.StatusCode)
	}
	if inv["total"] != "64.00" {
		t.Fatalf("invoice total = %v, want 64.00", inv["total"])
	}
	if inv["hash"] == "" || inv["hash"] == nil {
		t.Fatal("invoice must carry a verification hash")
	}

	// Usage endpoint agrees with preview.
	resp, usage := get(t, ts, "/v1/usage/"+custID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage status %d", resp.StatusCode)
	}
	if usage["projected_total"] != "64.00" {
		t.Fatalf("projected total = %v, want 64.00", usage["projected_total"])
	}
}

func TestEventMissingKeyRejected(t *testing.T) {
	ts, _ := testServer(t)
	resp, _ := post(t, ts, "/v1/events", map[string]any{
		"customer_id": "c", "meter_code": "m", "event_time": "2026-06-10T00:00:00Z",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing idempotency_key status = %d, want 400", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	ts, _ := testServer(t)
	resp, body := get(t, ts, "/healthz")
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("health = %d %v", resp.StatusCode, body)
	}
}

func TestUsageNoActiveSub(t *testing.T) {
	ts, _ := testServer(t)
	resp, _ := post(t, ts, "/v1/customers", map[string]any{"name": "Empty"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("setup customer failed")
	}
	resp, _ = get(t, ts, "/v1/usage/cus_does_not_exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("usage for no-sub customer = %d, want 404", resp.StatusCode)
	}
}
