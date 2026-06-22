// Package money is the single source of truth for monetary math in meterproof.
//
// Core principle (build plan §6): deterministic code does every cent of math,
// and floating point is forbidden — a hallucinated decimal in billing ends a
// business relationship. All amounts are exact decimals (shopspring/decimal).
//
// Rounding follows the fail-safe rule (§6 #4): when an amount must be reduced to
// the currency's minor unit, we round DOWN (toward zero / floor for positive
// amounts). The bias is always toward under-billing — we would rather eat a
// fraction of a cent than over-charge a customer by one.
package money

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Amount is an exact monetary quantity tagged with an ISO-4217 currency code.
// The zero value is an invalid amount (empty currency); construct via New/Parse.
type Amount struct {
	val      decimal.Decimal
	currency string
}

// minorUnitExponent maps a currency to the number of decimal places in its
// smallest unit. Currencies not listed default to 2. Zero-decimal currencies
// (JPY, KRW) and three-decimal currencies (BHD, KWD) are handled explicitly so
// rounding lands on a real payable unit.
var minorUnitExponent = map[string]int32{
	"JPY": 0, "KRW": 0, "VND": 0, "CLP": 0,
	"BHD": 3, "KWD": 3, "OMR": 3, "TND": 3,
}

// MinorUnitExponent returns the decimal places in the currency's smallest unit.
func MinorUnitExponent(currency string) int32 {
	if e, ok := minorUnitExponent[currency]; ok {
		return e
	}
	return 2
}

// New builds an Amount from a decimal value and currency. The value is stored
// exactly (not yet rounded to the minor unit) so intermediate math keeps full
// precision; round only at line/invoice boundaries via RoundDown.
func New(val decimal.Decimal, currency string) Amount {
	return Amount{val: val, currency: currency}
}

// Zero returns a zero amount in the given currency.
func Zero(currency string) Amount { return Amount{val: decimal.Zero, currency: currency} }

// FromString parses a decimal string (e.g. "12.50") into an Amount.
func FromString(s, currency string) (Amount, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, err)
	}
	return Amount{val: d, currency: currency}, nil
}

// Decimal returns the underlying exact value.
func (a Amount) Decimal() decimal.Decimal { return a.val }

// Currency returns the ISO-4217 code.
func (a Amount) Currency() string { return a.currency }

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a.val.IsZero() }

// sameCurrency guards every binary op: mixing currencies is a programming error
// that must never silently produce a wrong number.
func (a Amount) sameCurrency(b Amount) error {
	if a.currency != b.currency {
		return fmt.Errorf("money: currency mismatch %q vs %q", a.currency, b.currency)
	}
	return nil
}

// Add returns a+b. Panics on currency mismatch (a billing bug, not a runtime
// condition to recover from). Use TryAdd if you need an error instead.
func (a Amount) Add(b Amount) Amount {
	if err := a.sameCurrency(b); err != nil {
		panic(err)
	}
	return Amount{val: a.val.Add(b.val), currency: a.currency}
}

// Sub returns a-b.
func (a Amount) Sub(b Amount) Amount {
	if err := a.sameCurrency(b); err != nil {
		panic(err)
	}
	return Amount{val: a.val.Sub(b.val), currency: a.currency}
}

// MulQuantity multiplies the amount by a dimensionless quantity (e.g. a unit
// price times a usage count). The result keeps full precision.
func (a Amount) MulQuantity(qty decimal.Decimal) Amount {
	return Amount{val: a.val.Mul(qty), currency: a.currency}
}

// MulFraction scales the amount by a fraction in [0,1] (e.g. proration).
func (a Amount) MulFraction(frac decimal.Decimal) Amount {
	return Amount{val: a.val.Mul(frac), currency: a.currency}
}

// RoundDown reduces the amount to the currency's minor unit, rounding toward
// zero (floor for non-negative amounts). This is the fail-safe rounding from
// §6 #4: bias toward under-billing. Apply it at line and invoice boundaries,
// never to intermediate values.
func (a Amount) RoundDown() Amount {
	exp := MinorUnitExponent(a.currency)
	return Amount{val: a.val.Truncate(exp), currency: a.currency}
}

// Cmp compares two amounts: -1 if a<b, 0 if equal, +1 if a>b.
func (a Amount) Cmp(b Amount) int {
	if err := a.sameCurrency(b); err != nil {
		panic(err)
	}
	return a.val.Cmp(b.val)
}

// String renders the amount fixed to the currency's minor unit for display.
func (a Amount) String() string {
	return a.val.StringFixed(MinorUnitExponent(a.currency)) + " " + a.currency
}

// Amount renders just the numeric value, fixed to the currency's minor unit and
// with no currency suffix — the canonical form for money in API JSON. Using this
// everywhere keeps every endpoint consistent and correct per currency: "64.00"
// for USD, "64" for JPY (0 decimals), "64.000" for BHD (3 decimals).
func (a Amount) Amount() string {
	return a.val.StringFixed(MinorUnitExponent(a.currency))
}

// Format is the package-level convenience: render a decimal as a currency-correct
// JSON money string. Format(d, "USD") == "64.00".
func Format(val decimal.Decimal, currency string) string {
	return New(val, currency).Amount()
}

// MinorUnits returns the amount as an integer count of the currency's smallest
// unit (e.g. cents) — the form payment processors like Stripe require. The
// amount should already be RoundDown'd to the minor unit; any residual fraction
// is truncated toward zero, preserving the under-bill bias. Exact, no float.
func (a Amount) MinorUnits() int64 {
	exp := MinorUnitExponent(a.currency)
	return a.val.Shift(exp).Truncate(0).IntPart()
}

// Sum adds a slice of amounts; all must share one currency. Returns a zero
// amount in that currency for an empty slice is not possible (unknown currency),
// so callers pass the currency explicitly.
func Sum(currency string, amounts ...Amount) Amount {
	total := Zero(currency)
	for _, a := range amounts {
		total = total.Add(a)
	}
	return total
}
