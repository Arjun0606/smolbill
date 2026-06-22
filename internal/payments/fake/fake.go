// Package fake is an in-memory payments.Processor for tests and for running the
// engine end-to-end without a real Stripe key. It records every push and honors
// idempotency so the finalize path can be exercised deterministically.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
)

// Processor is a recording test double.
type Processor struct {
	mu          sync.Mutex
	byKey       map[string]payments.PushResult     // idempotency_key -> result
	byID        map[string]payments.FetchedInvoice // external id -> what it "billed"
	chargeQueue map[string][]payments.ChargeResult // external id -> programmed charge outcomes
	ChargeCalls map[string]int                     // external id -> attempts made
	Pushes      []payments.PushRequest             // every call, in order
	FailWith    error                              // if set, PushInvoice returns this
	ChargeFault error                              // if set, ChargeInvoice returns this as a transport error
	seq         int
}

// New returns an empty fake processor.
func New() *Processor {
	return &Processor{
		byKey:       map[string]payments.PushResult{},
		byID:        map[string]payments.FetchedInvoice{},
		chargeQueue: map[string][]payments.ChargeResult{},
		ChargeCalls: map[string]int{},
	}
}

// Name implements payments.Processor.
func (p *Processor) Name() string { return "fake" }

// PushInvoice records the request and returns a synthetic result, reusing the
// prior result for a repeated idempotency key (no duplicate "invoice").
func (p *Processor) PushInvoice(_ context.Context, req payments.PushRequest) (payments.PushResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.FailWith != nil {
		return payments.PushResult{}, p.FailWith
	}
	if res, ok := p.byKey[req.IdempotencyKey]; ok {
		return res, nil
	}
	p.seq++
	res := payments.PushResult{
		ExternalID: "fake_in_" + itoa(p.seq),
		Status:     "open",
		HostedURL:  "https://fake.invoices/" + itoa(p.seq),
	}
	p.byKey[req.IdempotencyKey] = res
	p.byID[res.ExternalID] = payments.FetchedInvoice{
		AmountMinor: money.New(req.Invoice.Total, req.Invoice.Currency).MinorUnits(),
		Currency:    req.Invoice.Currency,
		Status:      res.Status,
	}
	p.Pushes = append(p.Pushes, req)
	return res, nil
}

// FetchInvoice returns what the fake processor "billed" for an external id.
func (p *Processor) FetchInvoice(_ context.Context, externalID string) (payments.FetchedInvoice, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fi, ok := p.byID[externalID]
	if !ok {
		return payments.FetchedInvoice{}, fmt.Errorf("fake: no invoice %q", externalID)
	}
	return fi, nil
}

// ChargeInvoice pops the next programmed outcome for an external id (default:
// paid, if none programmed) and marks the invoice paid on success. A decline is
// returned as a ChargeResult, not an error — matching real processors.
func (p *Processor) ChargeInvoice(_ context.Context, externalID string) (payments.ChargeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ChargeFault != nil {
		return payments.ChargeResult{}, p.ChargeFault
	}
	if _, ok := p.byID[externalID]; !ok {
		return payments.ChargeResult{}, fmt.Errorf("fake: no invoice %q", externalID)
	}
	p.ChargeCalls[externalID]++
	res := payments.ChargeResult{Status: "paid"}
	if q := p.chargeQueue[externalID]; len(q) > 0 {
		res = q[0]
		p.chargeQueue[externalID] = q[1:]
	}
	if res.Status == "paid" {
		fi := p.byID[externalID]
		fi.Status = "paid"
		p.byID[externalID] = fi
	}
	return res, nil
}

// ProgramCharges queues the outcomes ChargeInvoice will return for an external id,
// in order — so a test can script "fail, fail, then succeed" (recovery) or a hard
// decline. Once the queue drains, ChargeInvoice defaults to paid.
func (p *Processor) ProgramCharges(externalID string, outcomes ...payments.ChargeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chargeQueue[externalID] = append(p.chargeQueue[externalID], outcomes...)
}

// Tamper rewrites the amount the fake reports for an external id — simulating a
// processor that billed something other than smolbill computed (a tax line, a
// manual edit). Used to prove the cross-boundary reconciler catches drift.
func (p *Processor) Tamper(externalID string, amountMinor int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fi := p.byID[externalID]
	fi.AmountMinor = amountMinor
	p.byID[externalID] = fi
}

// Count returns how many distinct pushes were recorded.
func (p *Processor) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Pushes)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
