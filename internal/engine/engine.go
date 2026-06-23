// Package engine holds the shared application logic that more than one delivery
// surface needs — today the HTTP API and the MCP server. Keeping it here means
// the agent-facing tools and the REST API compute invoices and build plans
// through the exact same code path, so they can never disagree (the whole point
// of smolbill).
package engine

import (
	"errors"
	"fmt"
	"time"

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
	return invoice.Calculate(sub, plan, meters, events, taxRate(st, sub.CustomerID))
}

// taxRate returns the customer's configured tax rate (percent), or zero. The same
// rate is used at finalize and at reconcile, so a tax line is provable like any
// other — and if the rate changes after finalize, reconcile flags the drift.
func taxRate(st store.Store, customerID string) decimal.Decimal {
	if c, ok, err := st.GetCustomer(customerID); err == nil && ok {
		return c.TaxRate
	}
	return decimal.Zero
}

// ComputeAsOf computes a subscription's invoice from only the events that had
// been ingested at or before asOf — the billing state exactly as it was known at
// a past moment. Because smolbill is event-sourced, this is a precise replay, not
// a stored snapshot: it answers "what did this bill look like on date X, before
// the late events arrived?" — the audit/dispute superpower no snapshot-based
// billing system has.
func ComputeAsOf(st store.Store, sub domain.Subscription, asOf time.Time) (invoice.Result, error) {
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
	asOfEvents := make([]domain.Event, 0, len(events))
	for _, e := range events {
		if !e.IngestedAt.After(asOf) {
			asOfEvents = append(asOfEvents, e)
		}
	}
	return invoice.Calculate(sub, plan, meters, asOfEvents, taxRate(st, sub.CustomerID))
}

// ComputeAsOfForActiveSub time-travels the customer's active subscription's bill
// to asOf.
func ComputeAsOfForActiveSub(st store.Store, customerID string, asOf time.Time) (invoice.Result, domain.Subscription, error) {
	subs, err := st.SubscriptionsForCustomer(customerID)
	if err != nil {
		return invoice.Result{}, domain.Subscription{}, err
	}
	for _, sub := range subs {
		if sub.Status == domain.SubActive {
			res, err := ComputeAsOf(st, sub, asOf)
			return res, sub, err
		}
	}
	return invoice.Result{}, domain.Subscription{}, ErrNoActiveSub
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
	MeterCode     string      `json:"meter_code"`
	Model         string      `json:"model"`
	Currency      string      `json:"currency"`
	UnitAmount    string      `json:"unit_amount"`
	FlatAmount    string      `json:"flat_amount"`
	Tiers         []TierInput `json:"tiers"`
	PackageSize   string      `json:"package_size"`
	Percentage    string      `json:"percentage"`
	MinimumAmount string      `json:"minimum_amount"`
	MaximumAmount string      `json:"maximum_amount"`
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
			PackageSize: parseDecOr0(pr.PackageSize), Percentage: parseDecOr0(pr.Percentage),
			MinimumAmount: parseDecOr0(pr.MinimumAmount), MaximumAmount: parseDecOr0(pr.MaximumAmount),
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

// --- simulate-against-history (the AI-native sandbox primitive) ---
//
// An agent proposes a pricing change; smolbill replays the customer's REAL event
// log for the current period against the proposed plan and diffs it against what
// they're billed today. Nothing is persisted. This is how a change is proven safe
// before it is ever committed — the sandbox the incumbents can't bolt on.

// LineDelta is the per-meter difference between the current and proposed bill.
type LineDelta struct {
	MeterCode      string          `json:"meter_code"`
	CurrentAmount  decimal.Decimal `json:"current_amount"`
	ProposedAmount decimal.Decimal `json:"proposed_amount"`
	Delta          decimal.Decimal `json:"delta"`
}

// SimulationResult is what a proposed plan WOULD bill against the customer's real
// usage this period, next to what they are billed today. Persists nothing.
type SimulationResult struct {
	CustomerID    string          `json:"customer_id"`
	PeriodStart   time.Time       `json:"period_start"`
	PeriodEnd     time.Time       `json:"period_end"`
	Currency      string          `json:"currency"`
	CurrentTotal  decimal.Decimal `json:"current_total"`
	ProposedTotal decimal.Decimal `json:"proposed_total"`
	Delta         decimal.Decimal `json:"delta"` // proposed - current
	Lines         []LineDelta     `json:"lines"`
	CurrentHash   string          `json:"current_hash"`
	ProposedHash  string          `json:"proposed_hash"`
}

// SimulatePlanChange computes what `proposed` would bill the customer for the
// current period — replayed against their real event log — and diffs it against
// their live plan. It writes nothing to the store; the same deterministic engine
// that finalizes invoices computes the preview, so the simulation can't disagree
// with what a real switch would produce.
func SimulatePlanChange(st store.Store, customerID string, proposed PlanInput) (SimulationResult, error) {
	current, sub, err := ComputeForActiveSub(st, customerID)
	if err != nil {
		return SimulationResult{}, err
	}
	proposedPlan, err := BuildPlan(proposed)
	if err != nil {
		return SimulationResult{}, err
	}
	// The proposed plan is a hypothetical version of THIS subscription's plan, so
	// it carries the sub's plan identity — the engine's plan/sub guard then treats
	// it as a legitimate re-pricing of the same subscription.
	proposedPlan.ID = sub.PlanID
	proposedPlan.Version = sub.PlanVersion
	meters, err := st.Meters()
	if err != nil {
		return SimulationResult{}, err
	}
	events, err := st.EventsForCustomer(sub.CustomerID)
	if err != nil {
		return SimulationResult{}, err
	}
	proposedRes, err := invoice.Calculate(sub, proposedPlan, meters, events, taxRate(st, customerID))
	if err != nil {
		return SimulationResult{}, err
	}
	return diffInvoices(customerID, current, proposedRes), nil
}

// diffInvoices builds the per-meter and total deltas between two computed bills.
func diffInvoices(customerID string, current, proposed invoice.Result) SimulationResult {
	byCode := map[string]*LineDelta{}
	order := []string{}
	get := func(code string) *LineDelta {
		if d, ok := byCode[code]; ok {
			return d
		}
		d := &LineDelta{MeterCode: code, CurrentAmount: decimal.Zero, ProposedAmount: decimal.Zero}
		byCode[code] = d
		order = append(order, code)
		return d
	}
	for _, l := range current.Invoice.Lines {
		get(l.MeterCode).CurrentAmount = l.Amount
	}
	for _, l := range proposed.Invoice.Lines {
		get(l.MeterCode).ProposedAmount = l.Amount
	}
	lines := make([]LineDelta, 0, len(order))
	for _, code := range order {
		d := byCode[code]
		d.Delta = d.ProposedAmount.Sub(d.CurrentAmount)
		lines = append(lines, *d)
	}
	return SimulationResult{
		CustomerID:    customerID,
		PeriodStart:   current.Invoice.PeriodStart,
		PeriodEnd:     current.Invoice.PeriodEnd,
		Currency:      current.Invoice.Currency,
		CurrentTotal:  current.Invoice.Total,
		ProposedTotal: proposed.Invoice.Total,
		Delta:         proposed.Invoice.Total.Sub(current.Invoice.Total),
		Lines:         lines,
		CurrentHash:   current.Hash,
		ProposedHash:  proposed.Hash,
	}
}
