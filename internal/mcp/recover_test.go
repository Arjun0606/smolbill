package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// A panicking tool must surface as an error result, never crash the connection.
func TestRunToolRecoversPanic(t *testing.T) {
	st := memory.New()
	s := New(st, ingest.New(st, 0), nil)
	boom := tool{name: "boom", handler: func(context.Context, json.RawMessage) (string, error) {
		panic("kaboom")
	}}
	_, err := s.runTool(context.Background(), boom, nil)
	if err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("want recovered internal error, got %v", err)
	}
}
