package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// LedgerRow is one row of the reconciliation ledger (build plan §8): the
// persisted proof that, for a given invoice line, the raw events, the meter
// value, and the invoice-line amount agreed at the moment the invoice was
// computed. The reconcile endpoint re-derives these from the live event log and
// compares — any disagreement is surfaced (it can never silently drift).
type LedgerRow struct {
	ID                string
	InvoiceID         string
	MeterCode         string
	RawEventCount     int
	MeterValue        decimal.Decimal
	InvoiceLineAmount decimal.Decimal
	EntitlementState  map[string]any // optional snapshot; nil in v0
	Diffs             []string       // non-empty => drift detected at compute time
	ComputedHash      string         // the invoice-level verification hash
	ComputedAt        time.Time
}

// Alert is a proactive spend-alert config: fire WebhookURL when a customer's
// projected current-period spend crosses each percentage in Thresholds of
// Budget. MaxFired is the high-water mark already notified, so each threshold
// fires at most once per period (no alert spam).
type Alert struct {
	ID         string
	CustomerID string
	Budget     decimal.Decimal
	Currency   string
	Thresholds []int
	WebhookURL string
	MaxFired   int
}

// EntitlementKind is whether an entitlement is an on/off feature flag or a
// metered allowance with a numeric limit.
type EntitlementKind string

const (
	EntBoolean EntitlementKind = "boolean"
	EntMetered EntitlementKind = "metered"
)

// Entitlement is a customer's access to a feature, optionally with a metered
// limit. For metered entitlements the live used_value is derived from the event
// log over [PeriodStart, PeriodEnd) via MeterCode — never trusted from a stored
// counter that could drift.
type Entitlement struct {
	ID          string
	CustomerID  string
	Feature     string
	Kind        EntitlementKind
	MeterCode   string          // for metered: which meter measures usage
	LimitValue  decimal.Decimal // for metered: the allowance
	UsedValue   decimal.Decimal // derived live; persisted only as a cache
	PeriodStart time.Time
	PeriodEnd   time.Time
}
