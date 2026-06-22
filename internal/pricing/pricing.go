// Package pricing turns a metered quantity into a money amount for a single
// price, deterministically. It is the cent-level math the AI is forbidden from
// touching (build plan §6 #1): the agent sets the rule (which model, which
// tiers), this code computes the money.
//
// Rounding: each price returns a full-precision amount. Rounding to the
// currency's minor unit happens once, at the invoice-line boundary (see the
// invoice package), using the under-bill rule (§6 #4). Rounding here too would
// double-round and risk drift.
package pricing

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/meterproof/internal/domain"
	"github.com/Arjun0606/meterproof/internal/money"
)

// Calculate returns the (full-precision) charge for a price given a metered
// quantity. It does not prorate — proration applies only to flat fees and is
// handled by the invoice package so per-usage charges are never scaled.
func Calculate(p domain.Price, quantity decimal.Decimal) (money.Amount, error) {
	if quantity.IsNegative() {
		return money.Amount{}, fmt.Errorf("pricing: negative quantity %s for price %q", quantity, p.ID)
	}
	switch p.Model {
	case domain.ModelFlat:
		return money.New(p.FlatAmount, p.Currency), nil

	case domain.ModelPerUnit:
		return money.New(p.UnitAmount, p.Currency).MulQuantity(quantity), nil

	case domain.ModelTieredGraduated:
		return graduated(p, quantity)

	case domain.ModelTieredVolume:
		return volume(p, quantity)

	default:
		return money.Amount{}, fmt.Errorf("pricing: unknown model %q for price %q", p.Model, p.ID)
	}
}

// graduated splits the quantity across tiers: the units that fall inside each
// tier are charged at that tier's unit_amount, plus that tier's flat_amount if
// any units reached it. This is the Stripe "graduated" semantics.
//
// Tiers must be ascending by UpTo, with exactly the final tier having a nil
// UpTo (unbounded). validateTiers enforces this.
func graduated(p domain.Price, quantity decimal.Decimal) (money.Amount, error) {
	if err := validateTiers(p.Tiers); err != nil {
		return money.Amount{}, err
	}
	total := money.Zero(p.Currency)
	remaining := quantity
	lower := decimal.Zero // exclusive lower bound already consumed by previous tiers

	for _, t := range p.Tiers {
		if remaining.LessThanOrEqual(decimal.Zero) {
			break
		}
		var tierCapacity decimal.Decimal
		if t.UpTo == nil {
			tierCapacity = remaining // unbounded final tier absorbs the rest
		} else {
			tierCapacity = t.UpTo.Sub(lower)
			lower = *t.UpTo
		}
		unitsInTier := decimal.Min(remaining, tierCapacity)
		if unitsInTier.LessThanOrEqual(decimal.Zero) {
			continue
		}
		lineForTier := money.New(t.UnitAmount, p.Currency).MulQuantity(unitsInTier)
		total = total.Add(lineForTier)
		// Flat fee applies once if any usage reaches this tier.
		if !t.FlatAmount.IsZero() {
			total = total.Add(money.New(t.FlatAmount, p.Currency))
		}
		remaining = remaining.Sub(unitsInTier)
	}
	return total, nil
}

// volume prices the entire quantity at the single tier the quantity lands in
// (the last tier whose lower bound it exceeds). This is Stripe "volume"
// semantics: cross a threshold and the whole bill re-rates at the cheaper rate.
func volume(p domain.Price, quantity decimal.Decimal) (money.Amount, error) {
	if err := validateTiers(p.Tiers); err != nil {
		return money.Amount{}, err
	}
	chosen := p.Tiers[len(p.Tiers)-1] // default to final unbounded tier
	for _, t := range p.Tiers {
		if t.UpTo == nil || quantity.LessThanOrEqual(*t.UpTo) {
			chosen = t
			break
		}
	}
	total := money.New(chosen.UnitAmount, p.Currency).MulQuantity(quantity)
	if !chosen.FlatAmount.IsZero() {
		total = total.Add(money.New(chosen.FlatAmount, p.Currency))
	}
	return total, nil
}

// validateTiers enforces the invariant the tiered models rely on: at least one
// tier, ascending UpTo bounds, and exactly the last tier unbounded. Catching a
// malformed plan here (loudly) is the fail-safe — a silently wrong tier ladder
// is exactly the kind of drift the engine exists to prevent.
func validateTiers(tiers []domain.Tier) error {
	if len(tiers) == 0 {
		return fmt.Errorf("pricing: tiered model requires at least one tier")
	}
	var prev *decimal.Decimal
	for i, t := range tiers {
		isLast := i == len(tiers)-1
		if t.UpTo == nil {
			if !isLast {
				return fmt.Errorf("pricing: only the final tier may be unbounded (tier %d)", i)
			}
			continue
		}
		if isLast {
			return fmt.Errorf("pricing: final tier must be unbounded (nil UpTo)")
		}
		if !t.UpTo.IsPositive() {
			return fmt.Errorf("pricing: tier %d UpTo must be positive, got %s", i, t.UpTo)
		}
		if prev != nil && t.UpTo.LessThanOrEqual(*prev) {
			return fmt.Errorf("pricing: tier %d UpTo %s not greater than previous %s", i, t.UpTo, prev)
		}
		prev = t.UpTo
	}
	return nil
}
