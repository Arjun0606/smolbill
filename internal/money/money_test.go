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

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic", name)
		}
	}()
	f()
}

func TestArithmetic(t *testing.T) {
	if got := New(d("49.00"), "USD").Add(New(d("15.00"), "USD")); !got.Decimal().Equal(d("64.00")) {
		t.Errorf("Add = %s, want 64.00", got.Decimal())
	}
	if got := New(d("64.00"), "USD").Sub(New(d("15.00"), "USD")); !got.Decimal().Equal(d("49.00")) {
		t.Errorf("Sub = %s, want 49.00", got.Decimal())
	}
	// MulQuantity keeps full precision — no premature rounding of a unit price.
	if got := New(d("0.001"), "USD").MulQuantity(d("15000")); !got.Decimal().Equal(d("15")) {
		t.Errorf("MulQuantity = %s, want 15", got.Decimal())
	}
	// MulFraction (proration) is value-exact.
	if got := New(d("49.00"), "USD").MulFraction(d("0.5")); !got.Decimal().Equal(d("24.5")) {
		t.Errorf("MulFraction = %s, want 24.5", got.Decimal())
	}
}

func TestSum(t *testing.T) {
	got := Sum("USD", New(d("10.00"), "USD"), New(d("20.00"), "USD"), New(d("0.50"), "USD"))
	if !got.Decimal().Equal(d("30.50")) {
		t.Errorf("Sum = %s, want 30.50", got.Decimal())
	}
	if e := Sum("USD"); !e.IsZero() || e.Currency() != "USD" {
		t.Errorf("empty Sum not a USD zero: %s", e.String())
	}
}

func TestCmp(t *testing.T) {
	a, b := New(d("10.00"), "USD"), New(d("20.00"), "USD")
	if a.Cmp(b) != -1 || b.Cmp(a) != 1 || a.Cmp(a) != 0 {
		t.Errorf("Cmp ordering wrong: a.b=%d b.a=%d a.a=%d", a.Cmp(b), b.Cmp(a), a.Cmp(a))
	}
}

func TestFromString(t *testing.T) {
	a, err := FromString("12.50", "USD")
	if err != nil || !a.Decimal().Equal(d("12.50")) || a.Currency() != "USD" {
		t.Errorf("FromString valid: err=%v val=%s", err, a.Decimal())
	}
	if _, err := FromString("not-money", "USD"); err == nil {
		t.Error("FromString invalid: expected an error")
	}
}

func TestZeroAndIsZero(t *testing.T) {
	if z := Zero("USD"); !z.IsZero() || z.Currency() != "USD" {
		t.Error("Zero/IsZero wrong")
	}
	if New(d("0.01"), "USD").IsZero() {
		t.Error("0.01 must not be zero")
	}
}

func TestMinorUnitExponent(t *testing.T) {
	for cur, want := range map[string]int32{
		"USD": 2, "EUR": 2, "INR": 2, "XYZ": 2, // default
		"JPY": 0, "KRW": 0, // zero-decimal
		"BHD": 3, "KWD": 3, // three-decimal
	} {
		if got := MinorUnitExponent(cur); got != want {
			t.Errorf("MinorUnitExponent(%s) = %d, want %d", cur, got, want)
		}
	}
}

func TestRoundDownByCurrency(t *testing.T) {
	if got := New(d("1234.99"), "JPY").RoundDown(); !got.Decimal().Equal(d("1234")) {
		t.Errorf("JPY RoundDown(1234.99) = %s, want 1234", got.Decimal())
	}
	if got := New(d("2.5009"), "KWD").RoundDown(); !got.Decimal().Equal(d("2.500")) {
		t.Errorf("KWD RoundDown(2.5009) = %s, want 2.500", got.Decimal())
	}
}

func TestMinorUnitsTruncatesResidual(t *testing.T) {
	// 12.349 -> 1234 cents, never rounded up to 1235 (under-bill bias).
	if got := New(d("12.349"), "USD").MinorUnits(); got != 1234 {
		t.Errorf("MinorUnits(12.349) = %d, want 1234", got)
	}
	// A computed price round-trips exactly: 0.001 * 15000 = 15 -> 1500.
	if got := New(d("0.001"), "USD").MulQuantity(d("15000")).MinorUnits(); got != 1500 {
		t.Errorf("MinorUnits(0.001*15000) = %d, want 1500", got)
	}
}

func TestNegativeTruncatesTowardZero(t *testing.T) {
	// A -12.345 credit truncates toward zero to -12.34 (merchant-conservative).
	if got := New(d("-12.345"), "USD").RoundDown(); !got.Decimal().Equal(d("-12.34")) {
		t.Errorf("RoundDown(-12.345) = %s, want -12.34", got.Decimal())
	}
}

func TestStringByCurrency(t *testing.T) {
	if s := New(d("49"), "USD").String(); s != "49.00 USD" {
		t.Errorf("String(USD) = %q, want '49.00 USD'", s)
	}
	if s := New(d("1000"), "JPY").String(); s != "1000 JPY" {
		t.Errorf("String(JPY) = %q, want '1000 JPY'", s)
	}
}

func TestSubAndCmpMismatchPanic(t *testing.T) {
	mustPanic(t, "Sub", func() { _ = New(d("1"), "USD").Sub(New(d("1"), "EUR")) })
	mustPanic(t, "Cmp", func() { _ = New(d("1"), "USD").Cmp(New(d("1"), "EUR")) })
}
