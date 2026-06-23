package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

func authServer(t *testing.T, keys ...string) *serverHandle {
	t.Helper()
	st := memory.New()
	srv := New(st, ingest.New(st, 0), fixedClock())
	if len(keys) > 0 {
		srv.SetAuthKeys(keys...)
	}
	return newHandleFrom(t, srv)
}

func req(t *testing.T, h *serverHandle, method, path, key string, body any) int {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r, _ = http.NewRequest(method, h.ts.URL+path, bytes.NewReader(b))
	} else {
		r, _ = http.NewRequest(method, h.ts.URL+path, nil)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestV1OpenWhenNoKeysConfigured(t *testing.T) {
	h := authServer(t) // no keys
	if code := req(t, h, http.MethodPost, "/v1/customers", "", map[string]any{"name": "Acme"}); code == http.StatusUnauthorized {
		t.Fatal("with no keys configured, /v1 must be open (got 401)")
	}
}

func TestV1RequiresValidKey(t *testing.T) {
	h := authServer(t, "secret")
	// No key, wrong key -> 401.
	if code := req(t, h, http.MethodPost, "/v1/customers", "", map[string]any{"name": "Acme"}); code != http.StatusUnauthorized {
		t.Fatalf("no key: status = %d, want 401", code)
	}
	if code := req(t, h, http.MethodPost, "/v1/customers", "wrong", map[string]any{"name": "Acme"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", code)
	}
	// Correct key -> not 401 (the request proceeds to the handler).
	if code := req(t, h, http.MethodPost, "/v1/customers", "secret", map[string]any{"name": "Acme"}); code == http.StatusUnauthorized {
		t.Fatal("correct key was rejected")
	}
}

func TestXAPIKeyHeaderAccepted(t *testing.T) {
	h := authServer(t, "secret")
	r, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/v1/analytics", nil)
	r.Header.Set("X-API-Key", "secret")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("X-API-Key was not accepted")
	}
}

func TestHealthAndDashboardStayOpen(t *testing.T) {
	h := authServer(t, "secret")
	if code := req(t, h, http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("/healthz must stay open, got %d", code)
	}
	if code := req(t, h, http.MethodGet, "/pricing", "", nil); code == http.StatusUnauthorized {
		t.Fatal("/pricing (marketing) must stay open")
	}
}
