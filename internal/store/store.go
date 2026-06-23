// Package store defines the persistence contract for the smolbill engine. Both
// the in-memory backend (tests, demo) and the Postgres backend satisfy it, so
// the API and engine never depend on a concrete database. Keeping every method
// error-returning lets the same call sites work against a real DB.
package store

import (
	"time"

	"github.com/shopspring/decimal"

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
	// NextInvoiceNumber returns the next human-readable sequential invoice number
	// (e.g. INV-000042), monotonic and unique. Gaps are allowed (a number is spent
	// even if the finalize that follows it fails); ordering is what matters.
	NextInvoiceNumber() (string, error)
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

	// --- outbound webhooks (Phase 6) ---

	PutWebhook(w domain.Webhook) error
	Webhooks() ([]domain.Webhook, error)

	// --- dunning / collections (Phase 8) ---

	PutCollection(c domain.Collection) error
	GetCollection(invoiceID string) (domain.Collection, bool, error)
	// CollectionsDue returns collections eligible for a charge attempt now: those
	// scheduled (never attempted) or retrying with a next_attempt_at at/before now.
	CollectionsDue(now time.Time) ([]domain.Collection, error)
	// Collections returns every collection (all statuses) — for analytics/reporting.
	Collections() ([]domain.Collection, error)

	// --- dunning message templates (Phase 10) ---

	PutMessageTemplate(t domain.MessageTemplate) error
	MessageTemplates() ([]domain.MessageTemplate, error)

	// --- wallet (Phase 4) ---

	// Wallet returns the customer's wallet, or ok=false if none exists yet.
	Wallet(customerID string) (domain.Wallet, bool, error)
	// TopUpWallet credits the wallet by amount, idempotently on idempotencyKey
	// (a repeated key is a no-op returning the current balance). Creates the
	// wallet in the given currency on first top-up.
	TopUpWallet(customerID string, amount decimal.Decimal, currency, reason, idempotencyKey string) (domain.Wallet, error)
	// WalletTransactions returns the wallet's ledger, newest first.
	WalletTransactions(customerID string) ([]domain.WalletTransaction, error)

	// --- dashboard reads (Phase 4) ---

	ListCustomers() ([]domain.Customer, error)
	InvoicesForCustomer(customerID string) ([]domain.Invoice, error)

	// ListPlans/ListSubscriptions/ListInvoices return every row, for an external
	// dashboard to read the full surface. Read-only; no money math.
	ListPlans() ([]domain.Plan, error)
	ListSubscriptions() ([]domain.Subscription, error)
	ListInvoices() ([]domain.Invoice, error)
}
