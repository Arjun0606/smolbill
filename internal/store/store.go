// Package store defines the persistence contract for the smolbill engine. Both
// the in-memory backend (tests, demo) and the Postgres backend satisfy it, so
// the API and engine never depend on a concrete database. Keeping every method
// error-returning lets the same call sites work against a real DB.
package store

import (
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// Store is the full persistence surface used by the HTTP API and CLI.
type Store interface {
	// --- ingest (also satisfies ingest.Store) ---

	// SeenKey reports whether an idempotency key was accepted within
	// [now-window, now].
	SeenKey(key string, now time.Time, window time.Duration) (bool, error)
	// AppendEvent durably stores an accepted event.
	AppendEvent(e domain.Event) error

	// --- entities ---

	PutCustomer(c domain.Customer) error
	GetCustomer(id string) (domain.Customer, bool, error)

	PutMeter(m domain.Meter) error
	// Meters returns all meters keyed by code (for invoice calculation).
	Meters() (map[string]domain.Meter, error)

	PutPlan(p domain.Plan) error
	GetPlan(id string) (domain.Plan, bool, error)

	PutSubscription(s domain.Subscription) error
	GetSubscription(id string) (domain.Subscription, bool, error)
	// SubscriptionsForCustomer returns the customer's subscriptions.
	SubscriptionsForCustomer(customerID string) ([]domain.Subscription, error)

	// EventsForCustomer returns all stored events for a customer.
	EventsForCustomer(customerID string) ([]domain.Event, error)

	// --- invoices + reconciliation ledger (Phase 2) ---

	// SaveFinalizedInvoice persists an invoice, its lines, and the
	// reconciliation ledger rows atomically. Ledger rows with empty IDs are
	// assigned one. This is the write side of `/invoices/finalize`.
	SaveFinalizedInvoice(inv domain.Invoice, ledger []domain.LedgerRow) error
	// GetInvoice returns a stored invoice with its lines.
	GetInvoice(id string) (domain.Invoice, bool, error)
	// GetLedger returns the reconciliation ledger rows for an invoice.
	GetLedger(invoiceID string) ([]domain.LedgerRow, error)

	// --- entitlements (Phase 2) ---

	PutEntitlement(e domain.Entitlement) error
	EntitlementsForCustomer(customerID string) ([]domain.Entitlement, error)

	// --- spend alerts (Phase 3) ---

	PutAlert(a domain.Alert) error
	AlertsForCustomer(customerID string) ([]domain.Alert, error)
	// UpdateAlertFired advances an alert's high-water mark of fired thresholds.
	UpdateAlertFired(alertID string, maxFired int) error
}
