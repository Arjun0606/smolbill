package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func TestMinorUnits(t *testing.T) {
	cases := []struct {
		val      string
		currency string
		want     int64
	}{
		{"49.00", "USD", 4900},
		{"15.00", "USD", 1500},
		{"0.00", "USD", 0},
		{"12.34", "USD", 1234},
		{"1000", "JPY", 1000},  // zero-decimal currency: yen are the minor unit
		{"2.500", "BHD", 2500}, // three-decimal currency
	}
	for _, c := range cases {
		got := New(d(c.val), c.currency).MinorUnits()
		if got != c.want {
			t.Errorf("MinorUnits(%s %s) = %d, want %d", c.val, c.currency, got, c.want)
		}
	}
}

func TestRoundDownNeverOverBills(t *testing.T) {
	// 12.345 must floor to 12.34, not round half-up to 12.35.
	got := New(d("12.345"), "USD").RoundDown()
	if !got.Decimal().Equal(d("12.34")) {
		t.Fatalf("RoundDown(12.345) = %s, want 12.34", got.Decimal())
	}
}

func TestCurrencyMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on currency mismatch")
		}
	}()
	_ = New(d("1"), "USD").Add(New(d("1"), "EUR"))
}
