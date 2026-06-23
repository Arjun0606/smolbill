package polar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/payments"
)

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

type capturedReq struct {
	path           string
	body           map[string]any
	idempotencyKey string
	auth           string
}

func newMockPolar(t *testing.T) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	mux := http.NewServeMux()
	capture := func(r *http.Request) capturedReq {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return capturedReq{path: r.URL.Path, body: body,
			idempotencyKey: r.Header.Get("Idempotency-Key"), auth: r.Header.Get("Authorization")}
	}
	respond := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("POST /v1/checkouts/", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "co_mock", "status": "open", "url": "https://polar.test/co_mock"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &reqs
}

func sampleReq() payments.PushRequest {
	return payments.PushRequest{
		Customer: domain.Customer{ID: "cus_1", Name: "Acme", ExternalID: "acme"},
		Invoice: domain.Invoice{
			ID: "inv_1", Currency: "USD", Total: d("64.00"),
			Lines: []domain.InvoiceLine{
				{MeterCode: "", Amount: d("49.00")},
				{MeterCode: "tokens", Amount: d("15.00")},
			},
		},
		IdempotencyKey: "inv_1", Hash: "deadbeef",
	}
}

func TestPushInvoiceHappyPath(t *testing.T) {
	ts, reqs := newMockPolar(t)
	c := New("polar_test_123", true, WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "co_mock" || res.Status != "open" || res.HostedURL != "https://polar.test/co_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect: 1 checkout = 1 call (total pushed as a single checkout amount).
	if len(*reqs) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(*reqs), *reqs)
	}
	for _, r := range *reqs {
		if r.auth != "Bearer polar_test_123" {
			t.Fatalf("missing/wrong auth on %s: %q", r.path, r.auth)
		}
	}

	// The checkout must carry the TOTAL in integer minor units (cents), not a float.
	checkout := (*reqs)[0]
	if checkout.path != "/v1/checkouts/" {
		t.Fatalf("path = %q, want /v1/checkouts/", checkout.path)
	}
	// JSON numbers decode to float64; 64.00 USD -> 6400 cents.
	if amt, ok := checkout.body["amount"].(float64); !ok || int64(amt) != 6400 {
		t.Fatalf("amount = %v, want 6400 (integer cents)", checkout.body["amount"])
	}
	if checkout.body["currency"] != "USD" {
		t.Fatalf("currency = %v, want USD", checkout.body["currency"])
	}
	if checkout.idempotencyKey != "inv_1:checkout" {
		t.Fatalf("checkout idempotency key = %q, want inv_1:checkout", checkout.idempotencyKey)
	}
	meta, _ := checkout.body["metadata"].(map[string]any)
	if meta["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", meta)
	}
	if meta["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", meta)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orders/co_mock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"amount": 6400, "currency": "usd", "status": "paid"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", true, WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "co_mock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "paid" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeInvoiceIsProcessorManaged(t *testing.T) {
	c := New("k", true)
	// MoR rail: off-session pull is unsupported and must surface as an error, not a
	// "failed" decline that smolbill dunning would keep retrying.
	if _, err := c.ChargeInvoice(context.Background(), "co_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a merchant-of-record rail")
	}
}

func TestPolarErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkouts/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": "invalid checkout payload",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", true, WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Polar 400")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("POLAR_ACCESS_TOKEN", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset token: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("POLAR_ACCESS_TOKEN", "polar_oat_x")
	t.Setenv("POLAR_SANDBOX", "true")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "polar" {
		t.Fatalf("set token: want a polar processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
