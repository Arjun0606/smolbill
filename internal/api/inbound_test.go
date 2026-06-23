package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments/stripe"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// A verified "payment succeeded" webhook drives the collection to paid through the
// same dunning machine as an off-session retry. A bad signature is rejected.
func TestProcessorWebhook(t *testing.T) {
	st := memory.New()
	s := New(st, ingest.New(st, 0), nil)
	secret := "whsec_x"
	s.SetProcessor(stripe.New("sk_test", stripe.WithWebhookSecret(secret)))

	if err := st.PutCollection(domain.Collection{
		InvoiceID: "inv_1", ExternalID: "in_1", Status: "retrying", Attempts: 1, Currency: "USD", AmountMinor: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"type":"invoice.payment_succeeded","data":{"object":{"metadata":{"smolbill_invoice_id":"inv_1"}}}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1700000000." + string(body)))
	sig := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil))

	// Valid → 200 and the collection is now paid.
	req := httptest.NewRequest(http.MethodPost, "/integrations/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	col, _, _ := st.GetCollection("inv_1")
	if col.Status != "paid" {
		t.Fatalf("collection should be paid, got %q", col.Status)
	}

	// Bad signature → 400, no action.
	bad := httptest.NewRequest(http.MethodPost, "/integrations/stripe/webhook", bytes.NewReader(body))
	bad.Header.Set("Stripe-Signature", "t=1700000000,v1=deadbeef")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, bad)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("bad signature should be 400, got %d", rec2.Code)
	}
}
