// Package invoice assembles a deterministic, exact invoice for one billing
// period from a subscription, its plan, and the raw events. This is the
// `/invoices/preview` engine from build plan §9: pure code, no AI, no money math
// done anywhere an agent can reach (§6 #1).
//
// The same inputs always produce the same invoice and the same verification
// hash, which is precisely what the reconciliation ledger (Phase 2) needs to
// prove the meter and the invoice never silently disagree.
package invoice

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/meter"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/pricing"
)

// Result is a computed invoice plus the per-line meter trace that the
// reconciliation ledger turns into an auditable record.
type Result struct {
	Invoice domain.Invoice
	Traces  []LineTrace
	Hash    string // sha256 over the canonical line set; the verification hash
}

// LineTrace records how one invoice line was derived: which meter, the raw
// event count it saw, the aggregated quantity, and the resulting amount. This is
// the raw-events -> meter -> invoice-line chain the ledger exposes.
type LineTrace struct {
	MeterCode       string
	PriceModel      domain.PriceModel
	RawEventCount   int
	MeterValue      decimal.Decimal
	ProrationFactor decimal.Decimal
	Amount          decimal.Decimal
}

// Calculate computes the invoice for sub over [PeriodStart, PeriodEnd) using the
// plan's prices and the supplied events and meter definitions.
//
//   - meters maps meter_code -> definition.
//   - events is the full event set; each price filters to its own meter/period.
//   - Flat prices (MeterCode == "") are prorated by the subscription's active
//     fraction of the period; usage prices are never prorated.
//   - Lines are emitted in a stable order (by meter code, then model) so the
//     hash is reproducible regardless of plan ordering.
func Calculate(
	sub domain.Subscription,
	plan domain.Plan,
	meters map[string]domain.Meter,
	events []domain.Event,
) (Result, error) {
	if plan.ID != sub.PlanID || plan.Version != sub.PlanVersion {
		return Result{}, fmt.Errorf("invoice: plan %s v%d does not match subscription's plan %s v%d",
			plan.ID, plan.Version, sub.PlanID, sub.PlanVersion)
	}

	currency := planCurrency(plan)
	periodStart, periodEnd := sub.CurrentPeriodStart, sub.CurrentPeriodEnd
	if !periodEnd.After(periodStart) {
		return Result{}, fmt.Errorf("invoice: period end %s not after start %s", periodEnd, periodStart)
	}

	// Active window for proration: from service start (clamped) until cancel.
	activeFrom := sub.StartedAt
	if activeFrom.IsZero() {
		activeFrom = periodStart
	}
	activeUntil := periodEnd
	if sub.CanceledAt != nil && sub.CanceledAt.Before(activeUntil) {
		activeUntil = *sub.CanceledAt
	}
	proration := prorationFactor(periodStart, periodEnd, activeFrom, activeUntil)

	var lines []domain.InvoiceLine
	var traces []LineTrace

	for _, p := range plan.Prices {
		if p.Currency != currency {
			return Result{}, fmt.Errorf("invoice: mixed currencies in plan %s (%s vs %s)", plan.ID, currency, p.Currency)
		}

		var quantity decimal.Decimal
		var rawCount int
		usedProration := decimal.NewFromInt(1)

		if p.Model == domain.ModelFlat {
			// Flat fee: quantity is conceptually 1, prorated by active fraction.
			quantity = decimal.NewFromInt(1)
			usedProration = proration
		} else {
			m, ok := meters[p.MeterCode]
			if !ok {
				return Result{}, fmt.Errorf("invoice: price %s references unknown meter %q", p.ID, p.MeterCode)
			}
			q, err := meter.Aggregate(m, events, periodStart, periodEnd)
			if err != nil {
				return Result{}, err
			}
			quantity = q
			rawCount = countEvents(p.MeterCode, events, periodStart, periodEnd)
		}

		amt, err := pricing.Calculate(p, quantity)
		if err != nil {
			return Result{}, err
		}
		if p.Model == domain.ModelFlat {
			amt = amt.MulFraction(usedProration)
		}

		// Round to the minor unit once, here, with the under-bill rule (§6 #4).
		amt = amt.RoundDown()

		line := domain.InvoiceLine{
			MeterCode: p.MeterCode,
			Quantity:  quantity,
			UnitPrice: representativeUnitPrice(p),
			Amount:    amt.Decimal(),
		}
		lines = append(lines, line)
		traces = append(traces, LineTrace{
			MeterCode:       p.MeterCode,
			PriceModel:      p.Model,
			RawEventCount:   rawCount,
			MeterValue:      quantity,
			ProrationFactor: usedProration,
			Amount:          amt.Decimal(),
		})
	}

	// Stable ordering for a reproducible hash.
	sort.SliceStable(lines, func(i, j int) bool { return lineKey(lines[i]) < lineKey(lines[j]) })
	sort.SliceStable(traces, func(i, j int) bool {
		return traces[i].MeterCode+string(traces[i].PriceModel) < traces[j].MeterCode+string(traces[j].PriceModel)
	})

	total := money.Zero(currency)
	for _, l := range lines {
		total = total.Add(money.New(l.Amount, currency))
	}

	inv := domain.Invoice{
		CustomerID:     sub.CustomerID,
		SubscriptionID: sub.ID,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		Status:         "preview",
		Lines:          lines,
		Total:          total.Decimal(),
		Currency:       currency,
	}
	return Result{Invoice: inv, Traces: traces, Hash: hashInvoice(inv)}, nil
}

func planCurrency(plan domain.Plan) string {
	if len(plan.Prices) == 0 {
		return "USD"
	}
	return plan.Prices[0].Currency
}

func countEvents(meterCode string, events []domain.Event, start, end time.Time) int {
	n := 0
	for _, e := range events {
		if e.MeterCode == meterCode && !e.EventTime.Before(start) && e.EventTime.Before(end) {
			n++
		}
	}
	return n
}

// representativeUnitPrice picks a single display unit price for a line. Tiered
// composites have no single rate, so they report zero; the trace carries the
// real breakdown.
func representativeUnitPrice(p domain.Price) decimal.Decimal {
	switch p.Model {
	case domain.ModelPerUnit:
		return p.UnitAmount
	case domain.ModelFlat:
		return p.FlatAmount
	default:
		return decimal.Zero
	}
}

func lineKey(l domain.InvoiceLine) string {
	return l.MeterCode + "|" + l.Quantity.String() + "|" + l.Amount.String()
}

// hashInvoice produces the verification hash over the canonical line set and
// total. Any drift in a quantity or amount changes the hash, which is the
// tamper-evidence the reconciliation ledger relies on.
func hashInvoice(inv domain.Invoice) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s\n",
		inv.CustomerID, inv.Currency,
		inv.PeriodStart.UTC().Format(time.RFC3339Nano),
		inv.PeriodEnd.UTC().Format(time.RFC3339Nano),
		inv.Total.String())
	for _, l := range inv.Lines {
		fmt.Fprintf(h, "%s|%s|%s|%s\n", l.MeterCode, l.Quantity.String(), l.UnitPrice.String(), l.Amount.String())
	}
	return hex.EncodeToString(h.Sum(nil))
}
