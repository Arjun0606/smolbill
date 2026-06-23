// Package meter aggregates raw usage events into a single billable quantity per
// meter per period. This is pure, deterministic code (build plan §6 #1): given
// the same events it always returns the same quantity, which is what makes the
// reconciliation ledger able to prove the meter and the invoice never disagree.
package meter

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// Aggregate computes the billable quantity for a meter over events that fall in
// the half-open period [start, end). Events outside the window, for other
// meters, or (in count's case) regardless of properties are handled per the
// meter's aggregation. The input slice is not mutated.
//
// Determinism notes:
//   - The period is half-open [start, end) so adjacent periods never double-count
//     a boundary event.
//   - "unique" iterates a sorted key set so the result is independent of map and
//     slice ordering.
func Aggregate(m domain.Meter, events []domain.Event, start, end time.Time) (decimal.Decimal, error) {
	inWindow := func(e domain.Event) bool {
		if e.MeterCode != m.Code {
			return false
		}
		// [start, end): start inclusive, end exclusive.
		return !e.EventTime.Before(start) && e.EventTime.Before(end)
	}

	switch m.Aggregation {
	case domain.AggCount:
		count := int64(0)
		for _, e := range events {
			if inWindow(e) {
				count++
			}
		}
		return decimal.NewFromInt(count), nil

	case domain.AggSum:
		sum := decimal.Zero
		for _, e := range events {
			if !inWindow(e) {
				continue
			}
			v, err := propertyDecimal(e, m.PropertyKey)
			if err != nil {
				return decimal.Zero, err
			}
			sum = sum.Add(v)
		}
		return sum, nil

	case domain.AggMax:
		var max decimal.Decimal
		seen := false
		for _, e := range events {
			if !inWindow(e) {
				continue
			}
			v, err := propertyDecimal(e, m.PropertyKey)
			if err != nil {
				return decimal.Zero, err
			}
			if !seen || v.GreaterThan(max) {
				max = v
				seen = true
			}
		}
		if !seen {
			return decimal.Zero, nil
		}
		return max, nil

	case domain.AggUnique:
		set := map[string]struct{}{}
		for _, e := range events {
			if !inWindow(e) {
				continue
			}
			v, ok := e.Properties[m.PropertyKey]
			if !ok {
				return decimal.Zero, fmt.Errorf("meter %q: event %q missing property %q", m.Code, e.IdempotencyKey, m.PropertyKey)
			}
			set[fmt.Sprintf("%v", v)] = struct{}{}
		}
		// Count is order-independent; sort only to keep behavior obviously deterministic.
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return decimal.NewFromInt(int64(len(keys))), nil

	default:
		return decimal.Zero, fmt.Errorf("meter %q: unknown aggregation %q", m.Code, m.Aggregation)
	}
}

// propertyDecimal pulls a numeric property out of an event as an exact decimal.
// JSON numbers arrive as float64; we route through decimal to avoid carrying
// float imprecision into money math. Strings that parse as numbers are accepted
// so SDKs can send "12.50" without surprises.
func propertyDecimal(e domain.Event, key string) (decimal.Decimal, error) {
	raw, ok := e.Properties[key]
	if !ok {
		return decimal.Zero, fmt.Errorf("event %q missing property %q", e.IdempotencyKey, key)
	}
	switch v := raw.(type) {
	case decimal.Decimal:
		return v, nil
	case int:
		return decimal.NewFromInt(int64(v)), nil
	case int64:
		return decimal.NewFromInt(v), nil
	case json.Number:
		// The request was decoded with UseNumber(), so the value arrived as its exact
		// source text — large integers (token counts past 2^53) and decimals keep full
		// precision, never passing through a float64. This is the float-free path.
		d, err := decimal.NewFromString(v.String())
		if err != nil {
			return decimal.Zero, fmt.Errorf("event %q property %q: %q is not numeric", e.IdempotencyKey, key, v.String())
		}
		return d, nil
	case float64:
		// Backstop for any decoder that didn't use UseNumber(). Route through the
		// shortest round-trip string instead of NewFromFloat so e.g. 0.1 stays exact.
		// (Precision lost in the float64 itself can't be recovered here — every request
		// path decodes with UseNumber so this is only a defensive fallback.)
		return decimal.NewFromString(strconv.FormatFloat(v, 'f', -1, 64))
	case string:
		d, err := decimal.NewFromString(v)
		if err != nil {
			return decimal.Zero, fmt.Errorf("event %q property %q: %q is not numeric", e.IdempotencyKey, key, v)
		}
		return d, nil
	default:
		return decimal.Zero, fmt.Errorf("event %q property %q: unsupported type %T", e.IdempotencyKey, key, raw)
	}
}
