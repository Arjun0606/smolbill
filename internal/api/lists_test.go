package api

import (
	"net/http"
	"testing"
)

// TestListEndpoints exercises the read-only list surface an external dashboard
// reads: after creating a customer and a plan, GET /v1/customers and /v1/plans
// must return the wrapped arrays, non-empty.
func TestListEndpoints(t *testing.T) {
	ts := newHandle(t)

	ts.post(t, "/v1/customers", map[string]any{"name": "Acme"})
	ts.post(t, "/v1/plans", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
		},
	})

	resp, body := ts.get(t, "/v1/customers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/customers status %d", resp.StatusCode)
	}
	customers, ok := body["customers"].([]any)
	if !ok {
		t.Fatalf("customers key missing or not an array: %v", body)
	}
	if len(customers) == 0 {
		t.Fatalf("customers array is empty, want >= 1")
	}

	resp, body = ts.get(t, "/v1/plans")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/plans status %d", resp.StatusCode)
	}
	plans, ok := body["plans"].([]any)
	if !ok {
		t.Fatalf("plans key missing or not an array: %v", body)
	}
	if len(plans) == 0 {
		t.Fatalf("plans array is empty, want >= 1")
	}
}
