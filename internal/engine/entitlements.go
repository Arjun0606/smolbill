package engine

import (
	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/meter"
	"github.com/Arjun0606/smolbill/internal/store"
)

// EntitlementStatus is a customer's live access to a feature: for metered
// entitlements the used value is derived from the event log over the period (not
// a trusted counter that could drift), so the limit check is always honest.
type EntitlementStatus struct {
	Feature     string `json:"feature"`
	Kind        string `json:"kind"`
	MeterCode   string `json:"meter_code,omitempty"`
	Limit       string `json:"limit,omitempty"`
	Used        string `json:"used,omitempty"`
	Remaining   string `json:"remaining,omitempty"`
	WithinLimit bool   `json:"within_limit"`
	PctUsed     string `json:"pct_used,omitempty"`
}

// CheckEntitlements returns the live status of every entitlement a customer has.
// Shared by the REST API and the MCP server so an agent and a human always see
// the same answer to "is this customer within their limit right now?".
func CheckEntitlements(st store.Store, customerID string) ([]EntitlementStatus, error) {
	ents, err := st.EntitlementsForCustomer(customerID)
	if err != nil {
		return nil, err
	}
	meters, err := st.Meters()
	if err != nil {
		return nil, err
	}
	events, err := st.EventsForCustomer(customerID)
	if err != nil {
		return nil, err
	}
	out := make([]EntitlementStatus, 0, len(ents))
	for _, e := range ents {
		o := EntitlementStatus{Feature: e.Feature, Kind: string(e.Kind), WithinLimit: true}
		if e.Kind == domain.EntMetered {
			used := decimal.Zero
			if m, ok := meters[e.MeterCode]; ok {
				used, _ = meter.Aggregate(m, events, e.PeriodStart, e.PeriodEnd)
			}
			o.MeterCode = e.MeterCode
			o.Limit = e.LimitValue.String()
			o.Used = used.String()
			o.Remaining = e.LimitValue.Sub(used).String()
			o.WithinLimit = used.LessThanOrEqual(e.LimitValue)
			if e.LimitValue.IsPositive() {
				o.PctUsed = used.Div(e.LimitValue).Mul(decimal.NewFromInt(100)).StringFixed(1)
			}
		}
		out = append(out, o)
	}
	return out, nil
}
