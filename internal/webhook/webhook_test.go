package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSignDeterministicAndVerifiable proves a receiver can independently verify a
// payload: signing the same body under the same secret is stable, and a different
// secret or a mutated body yields a different signature.
func TestSignDeterministicAndVerifiable(t *testing.T) {
	body := []byte(`{"type":"invoice.finalized"}`)
	sig := Sign("whsec_abc", body)
	if sig != Sign("whsec_abc", body) {
		t.Fatal("Sign must be deterministic for the same secret+body")
	}
	if sig == Sign("whsec_different", body) {
		t.Fatal("a different secret must change the signature")
	}
	if sig == Sign("whsec_abc", append(body, '!')) {
		t.Fatal("a tampered body must change the signature")
	}
}

// TestHTTPSenderDeliversSignedJSON proves the real transport POSTs a JSON event
// whose body verifies against the X-Smolbill-Signature header — exactly what a
// receiver's middleware would check.
func TestHTTPSenderDeliversSignedJSON(t *testing.T) {
	const secret = "whsec_test"
	type received struct {
		typeHeader string
		sigHeader  string
		body       []byte
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{r.Header.Get("X-Smolbill-Event"), r.Header.Get("X-Smolbill-Signature"), b}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ev := Event{ID: "evt_1", Type: "invoice.finalized", OccurredAt: time.Unix(0, 0).UTC(),
		Data: map[string]any{"invoice_id": "inv_1"}}
	if err := NewHTTPSender().Send(context.Background(), srv.URL, secret, ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case r := <-got:
		if r.typeHeader != "invoice.finalized" {
			t.Errorf("X-Smolbill-Event = %q, want invoice.finalized", r.typeHeader)
		}
		if want := Sign(secret, r.body); r.sigHeader != want {
			t.Errorf("signature header %q does not verify against body (want %q)", r.sigHeader, want)
		}
		var decoded Event
		if err := json.Unmarshal(r.body, &decoded); err != nil {
			t.Fatalf("body is not valid Event JSON: %v", err)
		}
		if decoded.Type != "invoice.finalized" || decoded.Data["invoice_id"] != "inv_1" {
			t.Errorf("delivered event mismatch: %+v", decoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery received")
	}
}

// TestHTTPSenderReturnsErrorOn500 proves a non-2xx is surfaced (so the emit loop
// logs a real failure rather than silently dropping it).
func TestHTTPSenderReturnsErrorOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := NewHTTPSender().Send(context.Background(), srv.URL, "s", Event{Type: "x"})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}
