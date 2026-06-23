// Package domain defines the core entities of the meterproof billing engine,
// mirroring the Postgres data model in build plan §8. These are plain value
// types: the deterministic engine (meter/pricing/invoice packages) operates on
// them, and the store packages persist them. Keeping them storage-agnostic is
// what makes the math layer unit-testable without a database.
//
// Every field carries a snake_case json tag so the HTTP API is uniformly
// snake_case in BOTH directions (requests and responses) — the convention every
// serious billing API (Stripe, Lago) uses, and what the typed SDKs are generated
// against. Without these tags responses would leak Go PascalCase field names.
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
	ModelPackage         PriceModel = "package"          // charge unit_amount per block of package_size units (rounded up)
	ModelPercentage      PriceModel = "percentage"       // percentage of the metered monetary base (e.g. marketplace commission)
)

// Customer is the billed entity.
type Customer struct {
	ID         string    `json:"id"`
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
}

// Meter defines how raw events aggregate into a billable quantity.
type Meter struct {
	ID          string      `json:"id"`
	Code        string      `json:"code"` // unique, referenced by prices and events
	Name        string      `json:"name"`
	Aggregation Aggregation `json:"aggregation"`
	PropertyKey string      `json:"property_key"` // which event property to aggregate (ignored for count)
	CreatedAt   time.Time   `json:"created_at"`
}

// Event is an immutable, idempotent usage record (the event-sourced core, §6 #2).
type Event struct {
	ID             string         `json:"id"`
	IdempotencyKey string         `json:"idempotency_key"` // unique; dedup happens on this (§6 #3)
	CustomerID     string         `json:"customer_id"`
	MeterCode      string         `json:"meter_code"`
	EventTime      time.Time      `json:"event_time"`
	Properties     map[string]any `json:"properties"`
	IngestedAt     time.Time      `json:"ingested_at"`
}

// Tier is one band in a tiered price. UpTo is the inclusive upper bound of the
// tier's quantity; a nil UpTo means "and everything above" (the final tier).
type Tier struct {
	UpTo       *decimal.Decimal `json:"up_to"`       // inclusive upper bound; nil = unbounded final tier
	UnitAmount decimal.Decimal  `json:"unit_amount"` // per-unit price within this tier
	FlatAmount decimal.Decimal  `json:"flat_amount"` // optional flat fee added if any units fall in this tier
}

// Price binds a meter to a pricing model within a plan.
type Price struct {
	ID         string          `json:"id"`
	PlanID     string          `json:"plan_id"`
	MeterCode  string          `json:"meter_code"` // empty for a pure flat subscription fee
	Model      PriceModel      `json:"model"`
	Currency   string          `json:"currency"`
	UnitAmount decimal.Decimal `json:"unit_amount"` // per_unit; also price-per-block for package
	FlatAmount decimal.Decimal `json:"flat_amount"` // for flat
	Tiers      []Tier          `json:"tiers"`       // for tiered_graduated / tiered_volume, ascending by UpTo
	// PackageSize is the block size for the package model: charge ceil(qty/size)
	// blocks at UnitAmount (e.g. $10 per 1,000 events).
	PackageSize decimal.Decimal `json:"package_size"`
	// Percentage is the rate for the percentage model, as a percent (2.5 = 2.5%) of
	// the metered monetary base — for a customer who resells / takes a commission.
	Percentage decimal.Decimal `json:"percentage"`
	// MinimumAmount floors this charge (minimum spend / commitment); zero = no floor.
	MinimumAmount decimal.Decimal `json:"minimum_amount"`
	// MaximumAmount caps this charge; zero = uncapped.
	MaximumAmount decimal.Decimal `json:"maximum_amount"`
}

// Plan is a versioned, immutable-once-published bundle of prices (§8).
type Plan struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Prices    []Price   `json:"prices"`
	CreatedAt time.Time `json:"created_at"`
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
	ID                 string     `json:"id"`
	CustomerID         string     `json:"customer_id"`
	PlanID             string     `json:"plan_id"`
	PlanVersion        int        `json:"plan_version"`
	Status             SubStatus  `json:"status"`
	CurrentPeriodStart time.Time  `json:"current_period_start"`
	CurrentPeriodEnd   time.Time  `json:"current_period_end"`
	StartedAt          time.Time  `json:"started_at"` // when service actually began (drives proration)
	CanceledAt         *time.Time `json:"canceled_at"`
}

// Invoice is a finalized bill for one period.
type Invoice struct {
	ID              string          `json:"id"`
	CustomerID      string          `json:"customer_id"`
	SubscriptionID  string          `json:"subscription_id"`
	PeriodStart     time.Time       `json:"period_start"`
	PeriodEnd       time.Time       `json:"period_end"`
	Status          string          `json:"status"`
	StripeInvoiceID string          `json:"external_invoice_id"` // processor-agnostic: id at whatever rail pushed it
	Lines           []InvoiceLine   `json:"lines"`
	Total           decimal.Decimal `json:"total"`
	Currency        string          `json:"currency"`
}

// InvoiceLine is one charge on an invoice, traceable back to a meter (LedgerRef).
type InvoiceLine struct {
	ID        string          `json:"id"`
	MeterCode string          `json:"meter_code"`
	Quantity  decimal.Decimal `json:"quantity"`
	UnitPrice decimal.Decimal `json:"unit_price"` // representative unit price (0 for flat/tiered composite)
	Amount    decimal.Decimal `json:"amount"`
	LedgerRef string          `json:"ledger_ref"` // links to the reconciliation ledger row
}

// Webhook is a registered HTTP endpoint that receives lifecycle events
// (invoice.finalized, drift.detected). Each carries a signing secret so the
// receiver can verify authenticity from the X-Smolbill-Signature HMAC header —
// the same model Stripe and Lago use, in the OSS core.
type Webhook struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"` // event types subscribed to; empty slice = all events
	Secret    string    `json:"secret"` // HMAC-SHA256 signing secret (returned once, on creation)
	CreatedAt time.Time `json:"created_at"`
}
