package engine

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/meter"
	"github.com/Arjun0606/smolbill/internal/store"
)

// GateRequest asks "may this customer do this right now?". Provide a Feature (to
// check a metered/boolean entitlement, optionally with a requested Quantity) and/or
// a Cost (to check against the prepaid wallet). At least one is required.
type GateRequest struct {
	CustomerID string
	Feature    string
	Quantity   decimal.Decimal // units requested against the feature's metered limit (default 1)
	Cost       decimal.Decimal // monetary cost to check against the wallet balance
	Currency   string
}

// GateDecision is the synchronous allow/deny, with the numbers behind it so the
// caller can show the user exactly why.
type GateDecision struct {
	Allowed   bool   `json:"allowed"`
	Reason    string `json:"reason"`
	Feature   string `json:"feature,omitempty"`
	Limit     string `json:"limit,omitempty"`
	Used      string `json:"used,omitempty"`
	Requested string `json:"requested,omitempty"`
	Remaining string `json:"remaining,omitempty"`
	Balance   string `json:"balance,omitempty"`
	Cost      string `json:"cost,omitempty"`
	Currency  string `json:"currency,omitempty"`
}

// Gate is real-time enforcement for the request hot path: a fast, synchronous
// allow/deny that HARD-BLOCKS when a metered entitlement would be exceeded or a
// prepaid wallet can't cover the cost — call it before you serve a request. Both
// sides are computed live (entitlement usage from the event log, balance from the
// wallet), so the gate can never drift from reality. This is request-level
// enforcement that even Metronome only does as after-the-fact alerts: spend caps
// warn; the gate denies.
func Gate(st store.Store, req GateRequest) (GateDecision, error) {
	if req.Feature == "" && req.Cost.IsZero() {
		return GateDecision{}, fmt.Errorf("gate: provide a feature and/or a cost to check")
	}
	d := GateDecision{Allowed: true, Reason: "within limits"}

	if req.Feature != "" {
		ents, err := st.EntitlementsForCustomer(req.CustomerID)
		if err != nil {
			return GateDecision{}, err
		}
		var ent *domain.Entitlement
		for i := range ents {
			if ents[i].Feature == req.Feature {
				ent = &ents[i]
				break
			}
		}
		if ent == nil {
			// No entitlement grants this feature -> deny (no access by default).
			return GateDecision{Allowed: false, Reason: "no entitlement grants this feature", Feature: req.Feature}, nil
		}
		d.Feature = ent.Feature
		if ent.Kind == domain.EntMetered {
			qty := req.Quantity
			if qty.IsZero() {
				qty = decimal.NewFromInt(1)
			}
			used, err := meteredUsage(st, req.CustomerID, *ent)
			if err != nil {
				return GateDecision{}, err
			}
			d.Limit = ent.LimitValue.String()
			d.Used = used.String()
			d.Requested = qty.String()
			d.Remaining = ent.LimitValue.Sub(used).String()
			if used.Add(qty).GreaterThan(ent.LimitValue) {
				d.Allowed = false
				d.Reason = "metered entitlement limit reached"
			}
		}
		// A boolean entitlement that exists is a grant -> stays allowed.
	}

	if d.Allowed && !req.Cost.IsZero() {
		cur := req.Currency
		if cur == "" {
			cur = "USD"
		}
		bal := decimal.Zero
		if w, ok, err := st.Wallet(req.CustomerID); err != nil {
			return GateDecision{}, err
		} else if ok {
			bal = w.Balance
		}
		d.Balance = bal.String()
		d.Cost = req.Cost.String()
		d.Currency = cur
		if bal.LessThan(req.Cost) {
			d.Allowed = false
			d.Reason = "insufficient prepaid balance"
		}
	}

	return d, nil
}

// meteredUsage computes a customer's live usage for an entitlement's meter over its
// period, straight from the event log (never a trusted counter that could drift).
func meteredUsage(st store.Store, customerID string, e domain.Entitlement) (decimal.Decimal, error) {
	meters, err := st.Meters()
	if err != nil {
		return decimal.Zero, err
	}
	m, ok := meters[e.MeterCode]
	if !ok {
		return decimal.Zero, nil
	}
	events, err := st.EventsForCustomer(customerID)
	if err != nil {
		return decimal.Zero, err
	}
	used, _ := meter.Aggregate(m, events, e.PeriodStart, e.PeriodEnd)
	return used, nil
}
