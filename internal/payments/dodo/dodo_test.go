package dodo

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

func newMockDodo(t *testing.T) (*httptest.Server, *[]capturedReq) {
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
	mux.HandleFunc("POST /customers", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"customer_id": "cus_mock"})
	})
	mux.HandleFunc("POST /payments", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"payment_id": "pay_mock", "status": "open", "payment_link": "https://dodo.test/pay_mock"})
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
	ts, reqs := newMockDodo(t)
	c := New("dodo_test_123", false, WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "pay_mock" || res.Status != "open" || res.HostedURL != "https://dodo.test/pay_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect: 1 customer + 1 payment = 2 calls (total pushed as one payment).
	if len(*reqs) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(*reqs), *reqs)
	}
	for _, r := range *reqs {
		if r.auth != "Bearer dodo_test_123" {
			t.Fatalf("missing/wrong auth on %s: %q", r.path, r.auth)
		}
	}

	// The payment must carry the TOTAL in integer minor units (cents), not a float.
	var pay capturedReq
	for _, r := range *reqs {
		if r.path == "/payments" {
			pay = r
		}
	}
	// JSON numbers decode to float64; 64.00 USD -> 6400 cents.
	if amt, ok := pay.body["amount"].(float64); !ok || int64(amt) != 6400 {
		t.Fatalf("amount = %v, want 6400 (integer cents)", pay.body["amount"])
	}
	if pay.body["currency"] != "USD" {
		t.Fatalf("currency = %v, want USD", pay.body["currency"])
	}
	if pay.idempotencyKey != "inv_1:payment" {
		t.Fatalf("payment idempotency key = %q, want inv_1:payment", pay.idempotencyKey)
	}
	meta, _ := pay.body["metadata"].(map[string]any)
	if meta["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", meta)
	}
	if meta["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", meta)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /payments/pay_mock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_amount": 6400, "currency": "usd", "status": "succeeded"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", false, WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "pay_mock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "succeeded" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeInvoiceIsProcessorManaged(t *testing.T) {
	c := New("k", false)
	// MoR rail: off-session pull is unsupported and must surface as an error, not a
	// "failed" decline that smolbill dunning would keep retrying.
	if _, err := c.ChargeInvoice(context.Background(), "pay_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a merchant-of-record rail")
	}
}

func TestDodoErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /customers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "invalid customer", "code": "validation_error"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", false, WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Dodo 400")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("DODO_PAYMENTS_API_KEY", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset key: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("DODO_PAYMENTS_API_KEY", "dodo_live_x")
	t.Setenv("DODO_PAYMENTS_LIVE", "true")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "dodo" {
		t.Fatalf("set key: want a dodo processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
