package stripe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	secret := "whsec_test"
	c := New("sk_test", WithWebhookSecret(secret))
	body := []byte(`{"type":"invoice.payment_succeeded","data":{"object":{"metadata":{"smolbill_invoice_id":"inv_1"}}}}`)

	// Valid signature → parsed event.
	h := http.Header{}
	h.Set("Stripe-Signature", sign(secret, "1700000000", body))
	ev, err := c.VerifyWebhook(h, body)
	if err != nil {
		t.Fatalf("valid signature should verify: %v", err)
	}
	if ev.Kind != "paid" || ev.InvoiceID != "inv_1" {
		t.Fatalf("got %+v, want paid/inv_1", ev)
	}

	// Tampered body (same header) → rejected.
	if _, err := c.VerifyWebhook(h, append(body, ' ')); err == nil {
		t.Fatal("tampered body must fail verification")
	}

	// Wrong signature → rejected.
	bad := http.Header{}
	bad.Set("Stripe-Signature", "t=1700000000,v1=deadbeef")
	if _, err := c.VerifyWebhook(bad, body); err == nil {
		t.Fatal("bad signature must fail")
	}

	// No secret configured → rejected (never accept an unverifiable event).
	if _, err := New("sk_test").VerifyWebhook(h, body); err == nil {
		t.Fatal("missing secret must fail")
	}

	// A failure event carries the decline reason.
	fb := []byte(`{"type":"charge.failed","data":{"object":{"failure_code":"card_declined","metadata":{"smolbill_invoice_id":"inv_2"}}}}`)
	fh := http.Header{}
	fh.Set("Stripe-Signature", sign(secret, "1700000000", fb))
	fev, err := c.VerifyWebhook(fh, fb)
	if err != nil || fev.Kind != "failed" || fev.Reason != "card_declined" {
		t.Fatalf("got %+v err=%v, want failed/card_declined", fev, err)
	}
}
