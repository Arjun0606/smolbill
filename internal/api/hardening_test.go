package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// A panic in a handler must become a clean 500, never crash the server.
func TestMiddlewareRecoversPanic(t *testing.T) {
	st := memory.New()
	s := New(st, ingest.New(st, 0), nil)
	h := s.withMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/anything", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on a handler panic, got %d", rec.Code)
	}
}
