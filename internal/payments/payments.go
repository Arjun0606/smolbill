// Package payments defines the processor-agnostic payment rail. smolbill sits on
// top of a processor (Stripe today; Paddle/Dodo pluggable later) for invoicing
// and charges only — it never holds funds, remits tax, or becomes a Merchant of
// Record (build plan §2, §6, §16). The deterministic engine computes the money;
// the processor merely collects it.
package payments

import (
	"context"
	"net/http"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// ProcessorEvent is a normalized inbound webhook from a payment rail, telling
// smolbill what actually happened to a charge it pushed. Real-world card
// collection is asynchronous (a charge can succeed, fail, or need authentication
// after the API call returns), so this is how the engine learns the true outcome.
type ProcessorEvent struct {
	// Kind: "paid" | "failed" | "action_required" | "ignored" (an event we don't act on).
	Kind string
	// InvoiceID is the smolbill invoice id (carried in the processor's metadata),
	// used to find the collection this event belongs to.
	InvoiceID string
	// Reason is the decline reason when Kind == "failed".
	Reason string
}

// InboundWebhooker is implemented by rails that can verify and parse their own
// inbound webhooks. It's optional: merchant-of-record rails manage collection
// themselves. VerifyWebhook MUST reject a request whose signature doesn't
// validate — the signature is the only authentication an inbound webhook carries.
type InboundWebhooker interface {
	VerifyWebhook(header http.Header, body []byte) (ProcessorEvent, error)
}

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
	// ChargeInvoice attempts to collect payment for an already-pushed invoice (an
	// off-session retry — the heart of dunning). A declined card is NOT a transport
	// error: it returns ChargeResult{Status:"failed", FailureReason: …} with a nil
	// error so the dunning engine can route by reason. err is non-nil only for a
	// genuine transport/processor fault (network, 5xx) where retrying the CALL —
	// not the card — is the right move.
	ChargeInvoice(ctx context.Context, externalID string) (ChargeResult, error)
}

// FetchedInvoice is what a processor reports it actually billed, in integer
// minor units (never a float) so it compares exactly against the ledger.
type FetchedInvoice struct {
	AmountMinor int64
	Currency    string
	Status      string
}

// ChargeResult is the outcome of a collection attempt. Status is "paid" or
// "failed"; FailureReason carries the processor's decline code (e.g.
// "insufficient_funds", "expired_card", "authentication_required") so dunning can
// decide whether a retry could ever succeed.
type ChargeResult struct {
	Status        string
	FailureReason string
}
