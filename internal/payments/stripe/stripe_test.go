package stripe

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

// mockStripe records requests and returns canned JSON, standing in for the real
// Stripe API so we can assert exactly what the adapter sends.
type capturedReq struct {
	path           string
	form           map[string]string
	idempotencyKey string
	auth           string
}

func newMockStripe(t *testing.T) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
	capture := func(r *http.Request) capturedReq {
		_ = r.ParseForm()
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		return capturedReq{path: r.URL.Path, form: form,
			idempotencyKey: r.Header.Get("Idempotency-Key"), auth: r.Header.Get("Authorization")}
	}
	mux.HandleFunc("POST /v1/customers", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "cus_mock"})
	})
	mux.HandleFunc("POST /v1/invoiceitems", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "ii_mock"})
	})
	mux.HandleFunc("POST /v1/invoices", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "in_mock"})
	})
	mux.HandleFunc("POST /v1/invoices/in_mock/finalize", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"id": "in_mock", "status": "open", "hosted_invoice_url": "https://stripe.test/in_mock"})
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
	ts, reqs := newMockStripe(t)
	c := New("sk_test_123", WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "in_mock" || res.Status != "open" || res.HostedURL != "https://stripe.test/in_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect: 1 customer + 2 invoice items + 1 invoice + 1 finalize = 5 calls.
	if len(*reqs) != 5 {
		t.Fatalf("calls = %d, want 5: %+v", len(*reqs), *reqs)
	}

	// Auth header on every call.
	for _, r := range *reqs {
		if r.auth != "Bearer sk_test_123" {
			t.Fatalf("missing/wrong auth on %s: %q", r.path, r.auth)
		}
	}

	// Amounts must be sent as INTEGER CENTS, not dollars/floats.
	var items []capturedReq
	for _, r := range *reqs {
		if r.path == "/v1/invoiceitems" {
			items = append(items, r)
		}
	}
	if len(items) != 2 {
		t.Fatalf("invoice items = %d, want 2", len(items))
	}
	amounts := map[string]bool{}
	for _, it := range items {
		amounts[it.form["amount"]] = true
		if it.form["currency"] != "usd" {
			t.Fatalf("currency = %q, want usd (lowercased)", it.form["currency"])
		}
	}
	if !amounts["4900"] || !amounts["1500"] {
		t.Fatalf("amounts in cents = %v, want 4900 and 1500", amounts)
	}

	// Idempotency keys must be distinct and derived from the invoice id.
	var invoiceCall capturedReq
	for _, r := range *reqs {
		if r.path == "/v1/invoices" {
			invoiceCall = r
		}
	}
	if invoiceCall.idempotencyKey != "inv_1:invoice" {
		t.Fatalf("invoice idempotency key = %q, want inv_1:invoice", invoiceCall.idempotencyKey)
	}
	if invoiceCall.form["metadata[reconciliation_hash]"] != "deadbeef" {
		t.Fatal("reconciliation hash not stamped on Stripe invoice metadata")
	}
}

func TestZeroAmountLineSkipped(t *testing.T) {
	ts, reqs := newMockStripe(t)
	c := New("sk_test", WithBaseURL(ts.URL))
	req := sampleReq()
	req.Invoice.Lines = []domain.InvoiceLine{
		{MeterCode: "tokens", Amount: d("0.00")}, // zero -> skipped
		{MeterCode: "calls", Amount: d("2.00")},
	}
	if _, err := c.PushInvoice(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	items := 0
	for _, r := range *reqs {
		if r.path == "/v1/invoiceitems" {
			items++
		}
	}
	if items != 1 {
		t.Fatalf("invoice items = %d, want 1 (zero line skipped)", items)
	}
}

func TestStripeErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/customers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "No such token", "type": "invalid_request_error"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("sk_bad", WithBaseURL(ts.URL))
	_, err := c.PushInvoice(context.Background(), sampleReq())
	if err == nil {
		t.Fatal("expected error from Stripe 400")
	}
}
