package paddle

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

func newMockPaddle(t *testing.T) (*httptest.Server, *[]capturedReq) {
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
		respond(w, map[string]any{"data": map[string]any{"id": "ctm_mock"}})
	})
	mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capture(r))
		respond(w, map[string]any{"data": map[string]any{
			"id":       "txn_mock",
			"status":   "open",
			"checkout": map[string]any{"url": "https://paddle.test/txn_mock"},
		}})
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
	ts, reqs := newMockPaddle(t)
	c := New("paddle_test_123", true, WithBaseURL(ts.URL))

	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "txn_mock" || res.Status != "open" || res.HostedURL != "https://paddle.test/txn_mock" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Expect: 1 customer + 1 transaction = 2 calls (total pushed as one transaction).
	if len(*reqs) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(*reqs), *reqs)
	}
	for _, r := range *reqs {
		if r.auth != "Bearer paddle_test_123" {
			t.Fatalf("missing/wrong auth on %s: %q", r.path, r.auth)
		}
	}

	// The transaction must carry the TOTAL in integer minor units (cents) as a
	// string, not a float. 64.00 USD -> "6400".
	var txn capturedReq
	for _, r := range *reqs {
		if r.path == "/transactions" {
			txn = r
		}
	}
	items, _ := txn.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1 item", txn.body["items"])
	}
	item, _ := items[0].(map[string]any)
	price, _ := item["price"].(map[string]any)
	unit, _ := price["unit_price"].(map[string]any)
	if unit["amount"] != "6400" {
		t.Fatalf("amount = %v, want \"6400\" (integer cents string)", unit["amount"])
	}
	if unit["currency_code"] != "USD" {
		t.Fatalf("unit currency = %v, want USD", unit["currency_code"])
	}
	if txn.body["currency_code"] != "USD" {
		t.Fatalf("currency_code = %v, want USD", txn.body["currency_code"])
	}
	if txn.idempotencyKey != "inv_1:txn" {
		t.Fatalf("transaction idempotency key = %q, want inv_1:txn", txn.idempotencyKey)
	}
	meta, _ := txn.body["custom_data"].(map[string]any)
	if meta["reconciliation_hash"] != "deadbeef" {
		t.Fatalf("reconciliation hash not stamped: %v", meta)
	}
	if meta["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("smolbill invoice id not stamped: %v", meta)
	}
}

func TestFetchInvoiceReconcilesOnTotal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /transactions/txn_mock", func(w http.ResponseWriter, _ *http.Request) {
		// Paddle Billing grand_total as an integer-minor-units string.
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"status":        "completed",
			"currency_code": "usd",
			"details": map[string]any{
				"totals": map[string]any{"grand_total": "6400", "currency_code": "usd"},
			},
		}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("k", true, WithBaseURL(ts.URL))
	fi, err := c.FetchInvoice(context.Background(), "txn_mock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "completed" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeInvoiceIsProcessorManaged(t *testing.T) {
	c := New("k", true)
	// MoR rail: off-session pull is unsupported and must surface as an error, not a
	// "failed" decline that smolbill dunning would keep retrying.
	if _, err := c.ChargeInvoice(context.Background(), "txn_mock"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a merchant-of-record rail")
	}
}

func TestPaddleErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /customers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"detail": "invalid customer", "code": "validation_error"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("bad", true, WithBaseURL(ts.URL))
	if _, err := c.PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from Paddle 400")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("PADDLE_API_KEY", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset key: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	t.Setenv("PADDLE_API_KEY", "pdl_live_x")
	t.Setenv("PADDLE_SANDBOX", "true")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "paddle" {
		t.Fatalf("set key: want a paddle processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
