package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Money-moving tools must PREVIEW (move nothing) until the engine is armed by the
// operator — the agent can never push money on its own.
func TestMoneyToolsArmingPreview(t *testing.T) {
	s, _ := newSession(t)
	cust, _ := s.callTool("create_customer", map[string]any{"name": "Acme"})
	custID := extractID(t, cust, "id ")

	// Not armed (default): top-up returns a preview and credits nothing.
	out, isErr := s.callTool("topup_wallet", map[string]any{"customer_id": custID, "amount": "50"})
	if isErr {
		t.Fatalf("topup errored: %s", out)
	}
	if !strings.Contains(out, "PREVIEW") || !strings.Contains(out, "Would credit") {
		t.Fatalf("expected preview, got: %s", out)
	}
	if w, _ := s.callTool("get_wallet", map[string]any{"customer_id": custID}); strings.Contains(w, "50") {
		t.Fatalf("wallet should be untouched while unarmed, got: %s", w)
	}

	// Armed: it actually credits.
	s.srv.SetSafety(true, decimal.Zero)
	out, _ = s.callTool("topup_wallet", map[string]any{"customer_id": custID, "amount": "50"})
	if !strings.Contains(out, "Credited") {
		t.Fatalf("armed top-up should credit, got: %s", out)
	}
}

// Armed money operations above the per-operation cap are refused.
func TestMoneyToolsCap(t *testing.T) {
	s, _ := newSession(t)
	cust, _ := s.callTool("create_customer", map[string]any{"name": "Acme"})
	custID := extractID(t, cust, "id ")

	s.srv.SetSafety(true, decimal.RequireFromString("10"))
	out, _ := s.callTool("topup_wallet", map[string]any{"customer_id": custID, "amount": "50"})
	if !strings.Contains(out, "exceeds the per-operation cap") {
		t.Fatalf("expected cap refusal, got: %s", out)
	}
	// Within the cap, it goes through.
	if ok, _ := s.callTool("topup_wallet", map[string]any{"customer_id": custID, "amount": "5"}); !strings.Contains(ok, "Credited") {
		t.Fatalf("within-cap top-up should credit, got: %s", ok)
	}
}

// The MCP HTTP transport exposes the full agent surface, so it must require a key
// when keys are configured — an open /mcp would hand the account to anyone.
func TestAPIKeyAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	guarded := APIKeyAuth([]string{"secret-key"}, inner)
	ts := httptest.NewServer(guarded)
	defer ts.Close()

	// No key → 401.
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key should be 401, got %d", resp.StatusCode)
	}

	// Valid key (both header forms) → 200.
	for _, set := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-API-Key", "secret-key") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret-key") },
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("valid key should be 200, got %d", resp.StatusCode)
		}
	}

	// Wrong key → 401.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("X-API-Key", "nope")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key should be 401, got %d", resp.StatusCode)
	}

	// No keys configured → open (zero-config dev).
	open := httptest.NewServer(APIKeyAuth(nil, inner))
	defer open.Close()
	resp, _ = http.Post(open.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no keys configured should pass through, got %d", resp.StatusCode)
	}
}
