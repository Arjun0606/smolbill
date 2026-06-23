package mcp

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// APIKeyAuth gates the MCP HTTP transport with the same API-key scheme as the /v1
// REST surface: a valid `Authorization: Bearer <key>` or `X-API-Key: <key>` header,
// constant-time compared. With no keys configured it passes through (zero-config
// dev) — main logs a warning so a public deployment isn't left open by accident.
//
// This matters because the MCP endpoint exposes the FULL agent surface; an open
// /mcp would let anyone who can reach it operate the billing account.
func APIKeyAuth(keys []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(keys) > 0 && !keyMatches(keys, r) {
			http.Error(w, "missing or invalid API key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func keyMatches(keys []string, r *http.Request) bool {
	got := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(got), []byte(k)) == 1 {
			return true
		}
	}
	return false
}
