// Package payments defines the processor-agnostic payment rail. smolbill sits on
// top of a processor (Stripe today; Paddle/Dodo pluggable later) for invoicing
// and charges only — it never holds funds, remits tax, or becomes a Merchant of
// Record (build plan §2, §6, §16). The deterministic engine computes the money;
// the processor merely collects it.
package payments

import (
	"context"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// PushRequest is a finalized smolbill invoice handed to a processor.
type PushRequest struct {
	Invoice  domain.Invoice
	Customer domain.Customer
	// IdempotencyKey makes the push safe to retry — the processor must not
	// create a duplicate invoice for the same key.
	IdempotencyKey string
	// Hash is the invoice's reconciliation verification hash, stamped on the
	// processor record so it links back to smolbill's proof.
	Hash string
}

// PushResult is what the processor returns once the invoice exists on its side.
type PushResult struct {
	ExternalID string // processor's invoice id (e.g. Stripe "in_...")
	Status     string // processor's invoice status (e.g. "open", "paid")
	HostedURL  string // customer-facing invoice URL, if any
}

// Processor is the pluggable payment rail. Implementations: stripe.Client (real)
// and fake.Processor (tests). The engine depends only on this interface.
type Processor interface {
	// Name identifies the processor (e.g. "stripe") for logging and audit.
	Name() string
	// PushInvoice creates (and finalizes) the invoice on the processor. It must
	// be idempotent on req.IdempotencyKey.
	PushInvoice(ctx context.Context, req PushRequest) (PushResult, error)
	// FetchInvoice reads back what the processor actually billed, so smolbill can
	// reconcile across the money rail: assert the processor's amount equals the
	// ledger's. This is the cross-boundary half of "the meter and the invoice
	// provably never disagree" — it catches drift the processor introduced
	// (a tax line, a rounding rule, a manual edit) that an internal ledger can't.
	FetchInvoice(ctx context.Context, externalID string) (FetchedInvoice, error)
}

// FetchedInvoice is what a processor reports it actually billed, in integer
// minor units (never a float) so it compares exactly against the ledger.
type FetchedInvoice struct {
	AmountMinor int64
	Currency    string
	Status      string
}
