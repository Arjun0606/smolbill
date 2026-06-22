// Package engine holds the shared application logic that more than one delivery
// surface needs — today the HTTP API and the MCP server. Keeping it here means
// the agent-facing tools and the REST API compute invoices and build plans
// through the exact same code path, so they can never disagree (the whole point
// of smolbill).
package engine

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/store"
)

// Errors returned by the compute helpers.
var (
	ErrNoActiveSub = errors.New("no active subscription for customer")
	ErrPlanGone    = errors.New("subscription references a missing plan")
)

// ComputeForActiveSub finds the customer's active subscription and computes its
// current-period invoice from the live event log.
func ComputeForActiveSub(st store.Store, customerID string) (invoice.Result, domain.Subscription, error) {
	subs, err := st.SubscriptionsForCustomer(customerID)
	if err != nil {
		return invoice.Result{}, domain.Subscription{}, err
	}
	for _, sub := range subs {
		if sub.Status == domain.SubActive {
			res, err := Compute(st, sub)
			return res, sub, err
		}
	}
	return invoice.Result{}, domain.Subscription{}, ErrNoActiveSub
}

// Compute runs the deterministic invoice engine for a subscription. This is the
// single place money math is invoked; no caller (API handler or MCP tool) ever
// computes money itself.
func Compute(st store.Store, sub domain.Subscription) (invoice.Result, error) {
	plan, ok, err := st.GetPlan(sub.PlanID)
	if err != nil {
		return invoice.Result{}, err
	}
	if !ok {
		return invoice.Result{}, ErrPlanGone
	}
	meters, err := st.Meters()
	if err != nil {
		return invoice.Result{}, err
	}
	events, err := st.EventsForCustomer(sub.CustomerID)
	if err != nil {
		return invoice.Result{}, err
	}
	return invoice.Calculate(sub, plan, meters, events)
}

// --- plan building (shared by REST and MCP) ---

// TierInput is one tier as supplied over the wire (decimal strings; nil UpTo for
// the unbounded final tier).
type TierInput struct {
	UpTo       *string `json:"up_to"`
	UnitAmount string  `json:"unit_amount"`
	FlatAmount string  `json:"flat_amount"`
}

// PriceInput is one price as supplied over the wire.
type PriceInput struct {
	MeterCode  string      `json:"meter_code"`
	Model      string      `json:"model"`
	Currency   string      `json:"currency"`
	UnitAmount string      `json:"unit_amount"`
	FlatAmount string      `json:"flat_amount"`
	Tiers      []TierInput `json:"tiers"`
}

// PlanInput is a plan as supplied over the wire.
type PlanInput struct {
	Name    string       `json:"name"`
	Version int          `json:"version"`
	Prices  []PriceInput `json:"prices"`
}

// BuildPlan validates a PlanInput and constructs a domain.Plan with fresh IDs.
// All decimal parsing happens here once, so REST and MCP get identical results.
func BuildPlan(in PlanInput) (domain.Plan, error) {
	if in.Name == "" || len(in.Prices) == 0 {
		return domain.Plan{}, fmt.Errorf("plan needs a name and at least one price")
	}
	version := in.Version
	if version == 0 {
		version = 1
	}
	plan := domain.Plan{ID: id.New("plan"), Name: in.Name, Version: version}
	for _, pr := range in.Prices {
		price := domain.Price{
			ID: id.New("price"), MeterCode: pr.MeterCode,
			Model: domain.PriceModel(pr.Model), Currency: pr.Currency,
			UnitAmount: parseDecOr0(pr.UnitAmount), FlatAmount: parseDecOr0(pr.FlatAmount),
		}
		for _, t := range pr.Tiers {
			tier := domain.Tier{UnitAmount: parseDecOr0(t.UnitAmount), FlatAmount: parseDecOr0(t.FlatAmount)}
			if t.UpTo != nil {
				v, err := decimal.NewFromString(*t.UpTo)
				if err != nil {
					return domain.Plan{}, fmt.Errorf("invalid tier up_to %q", *t.UpTo)
				}
				tier.UpTo = &v
			}
			price.Tiers = append(price.Tiers, tier)
		}
		plan.Prices = append(plan.Prices, price)
	}
	return plan, nil
}

func parseDecOr0(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
