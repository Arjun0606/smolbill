package meter

import (
	"encoding/json"
	"testing"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// The no-floats guarantee, end of the line: a usage quantity must never pass through
// a float64. A request decoded with UseNumber() delivers numbers as json.Number, and
// propertyDecimal must keep them exact — including integers past 2^53 that float64
// cannot represent. This is the regression guard for the "provably correct, no floats
// ever" claim.
func TestQuantityKeepsLargeIntPrecision(t *testing.T) {
	const big = "9007199254740993" // 2^53 + 1; as a float64 this rounds to ...992
	e := domain.Event{IdempotencyKey: "e1", Properties: map[string]any{"n": json.Number(big)}}
	got, err := propertyDecimal(e, "n")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != big {
		t.Fatalf("quantity = %s, want %s (a float64 path would round to 9007199254740992)", got.String(), big)
	}
}

func TestQuantityFloatBackstopIsExact(t *testing.T) {
	// If a float64 ever reaches propertyDecimal (a decoder without UseNumber), the
	// shortest-round-trip string keeps 0.1 exact rather than 0.1000000000000000055.
	e := domain.Event{IdempotencyKey: "e2", Properties: map[string]any{"n": 0.1}}
	got, err := propertyDecimal(e, "n")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "0.1" {
		t.Fatalf("float64 0.1 -> %s, want 0.1", got.String())
	}
}
