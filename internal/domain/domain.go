// Package domain defines the core entities of the meterproof billing engine,
// mirroring the Postgres data model in build plan §8. These are plain value
// types: the deterministic engine (meter/pricing/invoice packages) operates on
// them, and the store packages persist them. Keeping them storage-agnostic is
// what makes the math layer unit-testable without a database.
package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Aggregation is how a meter rolls up its events over a period.
type Aggregation string

const (
	AggCount  Aggregation = "count"  // number of events
	AggSum    Aggregation = "sum"    // sum of a numeric property
	AggMax    Aggregation = "max"    // max of a numeric property
	AggUnique Aggregation = "unique" // count of distinct property values
)

// PriceModel is how a price turns a metered quantity into money.
type PriceModel string

const (
	ModelFlat            PriceModel = "flat"             // fixed amount per period, usage-independent (prorated)
	ModelPerUnit         PriceModel = "per_unit"         // unit_amount * quantity
	ModelTieredGraduated PriceModel = "tiered_graduated" // quantity split across tiers, each portion at its rate
	ModelTieredVolume    PriceModel = "tiered_volume"    // whole quantity priced at the single tier it lands in
)

// Customer is the billed entity.
type Customer struct {
	ID         string
	ExternalID string
	Name       string
	CreatedAt  time.Time
}

// Meter defines how raw events aggregate into a billable quantity.
type Meter struct {
	ID          string
	Code        string // unique, referenced by prices and events
	Name        string
	Aggregation Aggregation
	PropertyKey string // which event property to aggregate (ignored for count)
	CreatedAt   time.Time
}

// Event is an immutable, idempotent usage record (the event-sourced core, §6 #2).
type Event struct {
	ID             string
	IdempotencyKey string // unique; dedup happens on this (§6 #3)
	CustomerID     string
	MeterCode      string
	EventTime      time.Time
	Properties     map[string]any
	IngestedAt     time.Time
}

// Tier is one band in a tiered price. UpTo is the inclusive upper bound of the
// tier's quantity; a nil UpTo means "and everything above" (the final tier).
type Tier struct {
	UpTo       *decimal.Decimal // inclusive upper bound; nil = unbounded final tier
	UnitAmount decimal.Decimal  // per-unit price within this tier
	FlatAmount decimal.Decimal  // optional flat fee added if any units fall in this tier
}

// Price binds a meter to a pricing model within a plan.
type Price struct {
	ID         string
	PlanID     string
	MeterCode  string // empty for a pure flat subscription fee
	Model      PriceModel
	Currency   string
	UnitAmount decimal.Decimal // for per_unit
	FlatAmount decimal.Decimal // for flat
	Tiers      []Tier          // for tiered_graduated / tiered_volume, ascending by UpTo
}

// Plan is a versioned, immutable-once-published bundle of prices (§8).
type Plan struct {
	ID        string
	Name      string
	Version   int
	Prices    []Price
	CreatedAt time.Time
}

// SubStatus is a subscription lifecycle state.
type SubStatus string

const (
	SubActive   SubStatus = "active"
	SubPaused   SubStatus = "paused"
	SubCanceled SubStatus = "canceled"
)

// Subscription attaches a plan version to a customer for a billing period.
type Subscription struct {
	ID                 string
	CustomerID         string
	PlanID             string
	PlanVersion        int
	Status             SubStatus
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	StartedAt          time.Time // when service actually began (drives proration)
	CanceledAt         *time.Time
}

// Invoice is a finalized bill for one period.
type Invoice struct {
	ID              string
	CustomerID      string
	SubscriptionID  string
	PeriodStart     time.Time
	PeriodEnd       time.Time
	Status          string
	StripeInvoiceID string
	Lines           []InvoiceLine
	Total           decimal.Decimal
	Currency        string
}

// InvoiceLine is one charge on an invoice, traceable back to a meter (LedgerRef).
type InvoiceLine struct {
	ID        string
	MeterCode string
	Quantity  decimal.Decimal
	UnitPrice decimal.Decimal // representative unit price (0 for flat/tiered composite)
	Amount    decimal.Decimal
	LedgerRef string // links to the reconciliation ledger row
}
