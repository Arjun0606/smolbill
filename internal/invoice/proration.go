package invoice

import (
	"time"

	"github.com/shopspring/decimal"
)

// prorationFactor returns the fraction of a billing period [periodStart,
// periodEnd) that a subscription was actually active, in [0,1].
//
// It is time-based (to the second), not day-based, so partial first/last days
// are handled exactly and the result is fully deterministic. Activity is the
// overlap of [activeFrom, activeUntil) with the period, divided by the period
// length.
//
// Used only for flat fees (build plan: per-usage charges are never prorated —
// you pay for exactly the usage you generated).
func prorationFactor(periodStart, periodEnd, activeFrom, activeUntil time.Time) decimal.Decimal {
	// Integer nanoseconds keep proration exact and float-free.
	periodNanos := periodEnd.Sub(periodStart).Nanoseconds()
	if periodNanos <= 0 {
		return decimal.Zero
	}

	overlapStart := periodStart
	if activeFrom.After(overlapStart) {
		overlapStart = activeFrom
	}
	overlapEnd := periodEnd
	if activeUntil.Before(overlapEnd) {
		overlapEnd = activeUntil
	}

	overlapNanos := overlapEnd.Sub(overlapStart).Nanoseconds()
	if overlapNanos <= 0 {
		return decimal.Zero
	}
	if overlapNanos >= periodNanos {
		return decimal.NewFromInt(1)
	}

	// High-precision exact division; the caller rounds money at the line boundary.
	return decimal.NewFromInt(overlapNanos).DivRound(decimal.NewFromInt(periodNanos), 12)
}
