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
	ID                string          `json:"id"`
	InvoiceID         string          `json:"invoice_id"`
	MeterCode         string          `json:"meter_code"`
	RawEventCount     int             `json:"raw_event_count"`
	MeterValue        decimal.Decimal `json:"meter_value"`
	InvoiceLineAmount decimal.Decimal `json:"invoice_line_amount"`
	EntitlementState  map[string]any  `json:"entitlement_state"` // optional snapshot; nil in v0
	Diffs             []string        `json:"diffs"`             // non-empty => drift detected at compute time
	ComputedHash      string          `json:"computed_hash"`     // the invoice-level verification hash
	ComputedAt        time.Time       `json:"computed_at"`
}

// Alert is a proactive spend-alert config: fire WebhookURL when a customer's
// projected current-period spend crosses each percentage in Thresholds of
// Budget. MaxFired is the high-water mark already notified, so each threshold
// fires at most once per period (no alert spam).
type Alert struct {
	ID         string          `json:"id"`
	CustomerID string          `json:"customer_id"`
	Budget     decimal.Decimal `json:"budget"`
	Currency   string          `json:"currency"`
	Thresholds []int           `json:"thresholds"`
	WebhookURL string          `json:"webhook_url"`
	MaxFired   int             `json:"max_fired"`
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
	ID          string          `json:"id"`
	CustomerID  string          `json:"customer_id"`
	Feature     string          `json:"feature"`
	Kind        EntitlementKind `json:"kind"`
	MeterCode   string          `json:"meter_code"`  // for metered: which meter measures usage
	LimitValue  decimal.Decimal `json:"limit_value"` // for metered: the allowance
	UsedValue   decimal.Decimal `json:"used_value"`  // derived live; persisted only as a cache
	PeriodStart time.Time       `json:"period_start"`
	PeriodEnd   time.Time       `json:"period_end"`
}
