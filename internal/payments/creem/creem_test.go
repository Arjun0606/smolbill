package creem

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
	apiKey         string
}

func newMockCreem(t *testing.T) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	mux := http.NewServeMux()
	capture := func(r *http.Request) capturedReq {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return capturedReq{path: r.URL.Path, body: body,
			idempotencyKey: r.Header.Get("Idempotency-Key"), apiKey: r.Header.Get("x-api-key")}
	}
	respond := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("POST /v1/checkouts", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "ch_mock", "status": "open", "checkout_url": "https://creem.test/ch_mock"})
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
	ts, reqs := newMockCreem(t)
	c := New("creem_test_123", true, WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "ch_mock" || res.Status != "open" || res.HostedURL != "https://creem.test/ch_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect: 1 checkout call (total pushed as one checkout).
	if len(*reqs) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(*reqs), *reqs)
	}
	// Auth is via the x-api-key header (NOT Bearer) on every call.
	for _, r := range *reqs {
		if r.apiKey != "creem_test_123" {
			t.Fatalf("missing/wrong x-api-key on %s: %q", r.path, r.apiKey)
		}
	}

	// The checkout must carry the TOTAL in integer minor units (cents), not a float.
	var co capturedReq
	for _, r := range *reqs {
		if r.path == "/v1/checkouts" {
			co = r
		}
	}
	// JSON numbers decode to float64; 64.00 USD -> 6400 cents.
	if amt, ok := co.body["amount"].(float64); !ok || int64(amt) != 6400 {
		t.Fatalf("amount = %v, want 6400 (integer cents)", co.body["amount"])
	}
	if co.body["currency"] != "USD" {
		t.Fatalf("currency = %v, want USD", co.body["currency"])
	}
	if co.idempotencyKey != "inv_1:checkout" {
		t.Fatalf("checkout idempotency key = %q, want inv_1:checkout", co.idempotencyKey)
	}
	meta, _ := co.body["metadata"].(map[string]any)
	if meta["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", meta)
	}
	if meta["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", meta)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/checkouts/ch_mock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"amount": 6400, "currency": "usd", "status": "paid"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", true, WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "ch_mock")
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
	if _, err := c.ChargeInvoice(context.Background(), "ch_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a merchant-of-record rail")
	}
}

func TestCreemErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkouts", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "invalid checkout", "code": "validation_error"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", true, WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Creem 400")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("CREEM_API_KEY", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset key: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("CREEM_API_KEY", "creem_live_x")
	t.Setenv("CREEM_TEST", "true")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "creem" {
		t.Fatalf("set key: want a creem processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
