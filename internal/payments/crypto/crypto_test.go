package crypto

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

func sampleReq() payments.PushRequest {
	return payments.PushRequest{
		Customer: domain.Customer{ID: "cus_1", Name: "Acme", ExternalID: "acme"},
		Invoice: domain.Invoice{
			ID: "inv_1", Currency: "USD", Total: d("64.00"),
			Lines: []domain.InvoiceLine{{MeterCode: "tokens", Amount: d("64.00")}},
		},
		IdempotencyKey: "inv_1", Hash: "deadbeef",
	}
}

func TestPushChargesTotalInMinorUnits(t *testing.T) {
	var body map[string]any
	var idem, auth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /charges", func(w http.ResponseWriter, r *http.Request) {
		idem = r.Header.Get("Idempotency-Key")
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "chg_1", "status": "pending", "pay_url": "https://pay.test/chg_1"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New("ck_test", ts.URL)
	res, err := c.PushInvoice(context.Background(), sampleReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "chg_1" || res.Status != "pending" || res.HostedURL != "https://pay.test/chg_1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if auth != "Bearer ck_test" {
		t.Fatalf("auth = %q", auth)
	}
	if idem != "inv_1:charge" {
		t.Fatalf("idempotency key = %q, want inv_1:charge", idem)
	}
	if amt, ok := body["amount"].(float64); !ok || int64(amt) != 6400 {
		t.Fatalf("amount = %v, want 6400 (integer minor units)", body["amount"])
	}
	if body["currency"] != "USD" {
		t.Fatalf("currency = %v, want USD", body["currency"])
	}
	meta, _ := body["metadata"].(map[string]any)
	if meta["reconciliation_hash"] != "deadbeef" || meta["smolbill_invoice_id"] != "inv_1" {
		t.Fatalf("metadata not stamped: %v", meta)
	}
}

func TestFetchReconcilesOnMinorUnits(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /charges/chg_1", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"amount_minor": 6400, "currency": "usd", "status": "settled"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	fi, err := New("k", ts.URL).FetchInvoice(context.Background(), "chg_1")
	if err != nil {
		t.Fatal(err)
	}
	if fi.AmountMinor != 6400 || fi.Currency != "USD" || fi.Status != "settled" {
		t.Fatalf("unexpected fetched invoice: %+v", fi)
	}
}

func TestChargeIsPushOnly(t *testing.T) {
	if _, err := New("k", "http://x").ChargeInvoice(context.Background(), "chg_1"); err == nil {
		t.Fatal("expected ChargeInvoice to error for a push-only crypto rail")
	}
}

func TestErrorSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /charges", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "settlement node unreachable"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if _, err := New("k", ts.URL).PushInvoice(context.Background(), sampleReq()); err == nil {
		t.Fatal("expected error from 502")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("CRYPTO_PAY_API_KEY", "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Fatalf("unset: want (nil,false,nil), got ok=%v err=%v", ok, err)
	}
	// Key set but no base URL is a configuration error.
	t.Setenv("CRYPTO_PAY_API_KEY", "ck")
	t.Setenv("CRYPTO_PAY_BASE_URL", "")
	if _, ok, err := FromEnv(); ok || err == nil {
		t.Fatalf("key without base url: want an error, got ok=%v err=%v", ok, err)
	}
	t.Setenv("CRYPTO_PAY_BASE_URL", "https://pay.example")
	p, ok, err := FromEnv()
	if !ok || err != nil || p == nil || p.Name() != "crypto" {
		t.Fatalf("configured: want a crypto processor, got ok=%v err=%v p=%v", ok, err, p)
	}
}
