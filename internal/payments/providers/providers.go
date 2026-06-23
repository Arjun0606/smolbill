// Package providers is the payment-rail registry: it turns environment
// configuration into a concrete payments.Processor without the rest of the binary
// knowing which rail is in play. The engine depends only on payments.Processor;
// only this package (and main) import the concrete adapters, so adding a rail is a
// one-line registry entry.
//
// Two ways a rail is chosen:
//   - Explicit: SMOLBILL_PROCESSOR=dodo (or stripe/paddle/lemonsqueezy/razorpay/
//     crypto) selects that rail and errors if its own env is incomplete.
//   - Auto: with SMOLBILL_PROCESSOR unset, the first rail whose env is configured
//     wins, in registry order (Stripe first for backward compatibility).
//
// Select returns (nil, nil) when nothing is configured — a valid state where
// finalize stays local-only (the ledger is still written, just not pushed).
package providers

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/payments/creem"
	"github.com/Arjun0606/smolbill/internal/payments/crypto"
	"github.com/Arjun0606/smolbill/internal/payments/dodo"
	"github.com/Arjun0606/smolbill/internal/payments/lemonsqueezy"
	"github.com/Arjun0606/smolbill/internal/payments/paddle"
	"github.com/Arjun0606/smolbill/internal/payments/polar"
	"github.com/Arjun0606/smolbill/internal/payments/razorpay"
	"github.com/Arjun0606/smolbill/internal/payments/stripe"
)

// fromEnvFunc is the registry contract: build the rail from its own env, returning
// ok=false (no error) when unconfigured so auto-detect can skip to the next.
type fromEnvFunc func() (payments.Processor, bool, error)

// registry is the ordered list of known rails. Order is the auto-detect priority;
// Stripe is first so existing STRIPE_SECRET_KEY deployments keep working unchanged.
var registry = []struct {
	name    string
	fromEnv fromEnvFunc
}{
	{"stripe", stripe.FromEnv},
	{"dodo", dodo.FromEnv},
	{"paddle", paddle.FromEnv},
	{"lemonsqueezy", lemonsqueezy.FromEnv},
	{"polar", polar.FromEnv},
	{"creem", creem.FromEnv},
	{"razorpay", razorpay.FromEnv},
	{"crypto", crypto.FromEnv},
}

// Names returns the known rail names, sorted, for help text and error messages.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.name)
	}
	sort.Strings(out)
	return out
}

// Select resolves the active processor from the environment. It returns:
//   - (proc, nil) when a rail is configured,
//   - (nil, nil)  when none is configured (finalize stays local-only),
//   - (nil, err)  when SMOLBILL_PROCESSOR names a rail that isn't fully configured
//     (or is unknown), or a configured rail fails to build.
func Select() (payments.Processor, error) {
	if want := strings.ToLower(strings.TrimSpace(os.Getenv("SMOLBILL_PROCESSOR"))); want != "" {
		for _, r := range registry {
			if r.name != want {
				continue
			}
			proc, ok, err := r.fromEnv()
			if err != nil {
				return nil, fmt.Errorf("payments: processor %q: %w", want, err)
			}
			if !ok {
				return nil, fmt.Errorf("payments: SMOLBILL_PROCESSOR=%s but its credentials are not set in the environment", want)
			}
			return proc, nil
		}
		return nil, fmt.Errorf("payments: unknown SMOLBILL_PROCESSOR %q (known: %s)", want, strings.Join(Names(), ", "))
	}

	// Auto-detect: first configured rail wins, in registry order.
	for _, r := range registry {
		proc, ok, err := r.fromEnv()
		if err != nil {
			return nil, fmt.Errorf("payments: processor %q: %w", r.name, err)
		}
		if ok {
			return proc, nil
		}
	}
	return nil, nil
}
