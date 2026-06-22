// Package fake is an in-memory payments.Processor for tests and for running the
// engine end-to-end without a real Stripe key. It records every push and honors
// idempotency so the finalize path can be exercised deterministically.
package fake

import (
	"context"
	"sync"

	"github.com/Arjun0606/smolbill/internal/payments"
)

// Processor is a recording test double.
type Processor struct {
	mu       sync.Mutex
	byKey    map[string]payments.PushResult // idempotency_key -> result
	Pushes   []payments.PushRequest         // every call, in order
	FailWith error                          // if set, PushInvoice returns this
	seq      int
}

// New returns an empty fake processor.
func New() *Processor {
	return &Processor{byKey: map[string]payments.PushResult{}}
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
	p.Pushes = append(p.Pushes, req)
	return res, nil
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
