package razorpay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func newMockRazorpay(t *testing.T) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	mux := http.NewServeMux()
	capture := func(r *http.Request) capturedReq {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return capturedReq{path: r.URL.Path, body: body,
			idempotencyKey: r.Header.Get("X-Razorpay-Idempotency"), auth: r.Header.Get("Authorization")}
	}
	respond := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("POST /invoices", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "inv_mock", "status": "open", "short_url": "https://rzp.io/i/inv_mock"})
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
	ts, reqs := newMockRazorpay(t)
	c := New("rzp_test_key", "secret_123", WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "inv_mock" || res.Status != "open" || res.HostedURL != "https://rzp.io/i/inv_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	if len(*reqs) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(*reqs), *reqs)
	}
	r := (*reqs)[0]

	// HTTP BASIC auth — not a bearer token.
	if !strings.HasPrefix(r.auth, "Basic ") {
		t.Fatalf("auth = %q, want a Basic auth header", r.auth)
	}

	// Idempotency header derived from the request key.
	if r.idempotencyKey != "inv_1:invoice" {
		t.Fatalf("idempotency key = %q, want inv_1:invoice", r.idempotencyKey)
	}

	if r.body["currency"] != "USD" {
		t.Fatalf("currency = %v, want USD", r.body["currency"])
	}

	// The line item must carry the TOTAL in integer minor units (cents), not a float.
	lineItems, ok := r.body["line_items"].([]any)
	if !ok || len(lineItems) != 1 {
		t.Fatalf("line_items = %v, want one item", r.body["line_items"])
	}
	li, _ := lineItems[0].(map[string]any)
	// JSON numbers decode to float64; 64.00 USD -> 6400 cents.
	if amt, ok := li["amount"].(float64); !ok || int64(amt) != 6400 {
		t.Fatalf("line amount = %v, want 6400 (integer cents)", li["amount"])
	}

	notes, _ := r.body["notes"].(map[string]any)
	if notes["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", notes)
	}
	if notes["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", notes)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /invoices/inv_mock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"amount": 6400, "currency": "usd", "status": "paid"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", "s", WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "inv_mock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "paid" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeInvoiceUnsupported(t *testing.T) {
	c := New("k", "s")
	// No saved token/mandate: off-session pull is unsupported and must surface as an
	// error, not a "failed" decline that smolbill dunning would keep retrying.
	if _, err := c.ChargeInvoice(context.Background(), "inv_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error without a saved token/mandate")
	}
}

func TestRazorpayErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /invoices", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"description": "invalid invoice", "code": "BAD_REQUEST_ERROR"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", "secret", WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Razorpay 400")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("RAZORPAY_KEY_ID", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset key: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("RAZORPAY_KEY_ID", "rzp_live_x")
	t.Setenv("RAZORPAY_KEY_SECRET", "secret_x")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "razorpay" {
		t.Fatalf("set key: want a razorpay processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
