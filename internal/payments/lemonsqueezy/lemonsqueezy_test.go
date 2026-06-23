package lemonsqueezy

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
	accept         string
	contentType    string
}

func newMockLemon(t *testing.T) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	mux := http.NewServeMux()
	capture := func(r *http.Request) capturedReq {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return capturedReq{
			path:           r.URL.Path,
			body:           body,
			idempotencyKey: r.Header.Get("Idempotency-Key"),
			auth:           r.Header.Get("Authorization"),
			accept:         r.Header.Get("Accept"),
			contentType:    r.Header.Get("Content-Type"),
		}
	}
	respond := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("POST /checkouts", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{
			"data": map[string]any{
				"id":   "chk_mock",
				"type": "checkouts",
				"attributes": map[string]any{
					"url":    "https://smolbill.lemonsqueezy.com/checkout/chk_mock",
					"status": "open",
				},
			},
		})
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
	ts, reqs := newMockLemon(t)
	c := New("ls_test_123", "store_42", WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "chk_mock" || res.Status != "open" ||
		res.HostedURL != "https://smolbill.lemonsqueezy.com/checkout/chk_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect a single checkout call carrying the whole total.
	if len(*reqs) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(*reqs), *reqs)
	}
	chk := (*reqs)[0]
	if chk.auth != "Bearer ls_test_123" {
		t.Fatalf("missing/wrong auth: %q", chk.auth)
	}
	if chk.accept != "application/vnd.api+json" || chk.contentType != "application/vnd.api+json" {
		t.Fatalf("missing JSON:API media headers: accept=%q content-type=%q", chk.accept, chk.contentType)
	}
	if chk.idempotencyKey != "inv_1:checkout" {
		t.Fatalf("idempotency key = %q, want inv_1:checkout", chk.idempotencyKey)
	}

	// JSON:API document: data.attributes.custom_price must be integer cents.
	data, _ := chk.body["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	// JSON numbers decode to float64; 64.00 USD -> 6400 cents.
	if cp, ok := attrs["custom_price"].(float64); !ok || int64(cp) != 6400 {
		t.Fatalf("custom_price = %v, want 6400 (integer cents)", attrs["custom_price"])
	}
	checkoutData, _ := attrs["checkout_data"].(map[string]any)
	custom, _ := checkoutData["custom"].(map[string]any)
	if custom["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", custom)
	}
	if custom["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", custom)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/chk_mock", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":   "chk_mock",
				"type": "orders",
				"attributes": map[string]any{
					"total":    6400,
					"currency": "usd",
					"status":   "paid",
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", "store_42", WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "chk_mock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "paid" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeInvoiceIsProcessorManaged(t *testing.T) {
	c := New("k", "store_42")
	// MoR rail: off-session pull is unsupported and must surface as an error, not a
	// "failed" decline that smolbill dunning would keep retrying.
	if _, err := c.ChargeInvoice(context.Background(), "chk_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a merchant-of-record rail")
	}
}

func TestLemonErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkouts", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{
				{"detail": "The store id is invalid.", "title": "Validation Error", "status": "422"},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", "store_42", WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Lemon Squeezy 422")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("LEMONSQUEEZY_API_KEY", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset key: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("LEMONSQUEEZY_API_KEY", "ls_live_x")
	t.Setenv("LEMONSQUEEZY_STORE_ID", "store_42")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "lemonsqueezy" {
		t.Fatalf("set key: want a lemonsqueezy processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
