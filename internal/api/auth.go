package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// SetAuthKeys turns on API-key auth for the /v1 surface. When at least one key is
// set, every /v1 request must present a matching key via `Authorization: Bearer
// <key>` or `X-API-Key: <key>`. With no keys set, /v1 is open (zero-config dev) —
// main logs a warning so production isn't left open by accident.
func (s *Server) SetAuthKeys(keys ...string) {
	s.authKeys = s.authKeys[:0]
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			s.authKeys = append(s.authKeys, k)
		}
	}
}

// SetRateLimit caps POST /v1/events at rps requests/second per key (burst = the
// bucket depth). Zero rps disables it.
func (s *Server) SetRateLimit(rps float64, burst int) {
	if rps <= 0 {
		s.limiter = nil
		return
	}
	s.limiter = newRateLimiter(rps, burst)
}

// presentedKey extracts the API key from the standard headers.
func presentedKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// authorized reports whether the request carries a valid key (or auth is off).
// Comparison is constant-time to avoid leaking key bytes via timing.
func (s *Server) authorized(r *http.Request) bool {
	if len(s.authKeys) == 0 {
		return true
	}
	got := []byte(presentedKey(r))
	for _, k := range s.authKeys {
		if subtle.ConstantTimeCompare(got, []byte(k)) == 1 {
			return true
		}
	}
	return false
}

// rateKey identifies a client for rate limiting: the API key if present, else the
// remote address.
func rateKey(r *http.Request) string {
	if k := presentedKey(r); k != "" {
		return "k:" + k
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return "ip:" + host
}

// withMiddleware wraps the routed mux with API-key auth (gating /v1 only — health,
// dashboard, portal, and the marketing/funnel pages stay open) and rate limiting on
// the ingest hot path.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && !s.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		if s.limiter != nil && r.Method == http.MethodPost && r.URL.Path == "/v1/events" {
			if !s.limiter.allow(rateKey(r)) {
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
