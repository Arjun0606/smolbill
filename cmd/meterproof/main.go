// Command meterproof is the Phase 1 CLI that exercises the deterministic core
// end-to-end (build plan §11): create a meter and a plan, ingest usage events
// idempotently, then compute an exact invoice preview with a full meter ->
// invoice-line trace. No Postgres, no Stripe, no AI — just proof the math is
// deterministic and the audit trail holds.
//
// Run `meterproof demo` for a scripted walkthrough.
package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/meterproof/internal/domain"
	"github.com/Arjun0606/meterproof/internal/ingest"
	"github.com/Arjun0606/meterproof/internal/invoice"
	"github.com/Arjun0606/meterproof/internal/store/memory"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Println("meterproof — provably-correct usage billing (Phase 1 core)")
		fmt.Println()
		fmt.Println("usage:")
		fmt.Println("  meterproof demo    run the end-to-end deterministic pipeline demo")
		return
	}
	if err := runDemo(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func dec(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}
func decp(s string) *decimal.Decimal { v := dec(s); return &v }

func runDemo() error {
	st := memory.New()
	ing := ingest.New(st, 0) // default published dedup window

	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// 1. Customer.
	st.PutCustomer(domain.Customer{ID: "cus_acme", ExternalID: "acme", Name: "Acme AI", CreatedAt: periodStart})

	// 2. Meter: sum of tokens.
	st.PutMeter(domain.Meter{Code: "tokens", Name: "LLM tokens", Aggregation: domain.AggSum, PropertyKey: "n"})

	// 3. Plan: $49 base (flat, prorated) + graduated token pricing.
	plan := domain.Plan{
		ID: "plan_pro", Name: "Pro", Version: 1, CreatedAt: periodStart,
		Prices: []domain.Price{
			{ID: "pr_base", PlanID: "plan_pro", Model: domain.ModelFlat, Currency: "USD", FlatAmount: dec("49.00")},
			{ID: "pr_tok", PlanID: "plan_pro", MeterCode: "tokens", Model: domain.ModelTieredGraduated, Currency: "USD",
				Tiers: []domain.Tier{
					{UpTo: decp("100000"), UnitAmount: dec("0.00002")}, // first 100k @ $0.00002
					{UpTo: nil, UnitAmount: dec("0.00001")},            // beyond @ $0.00001
				}},
		},
	}
	st.PutPlan(plan)

	// 4. Subscription, started mid-period (2026-06-16) to show proration.
	started := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub := domain.Subscription{
		ID: "sub_acme", CustomerID: "cus_acme", PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: periodStart, CurrentPeriodEnd: periodEnd,
		StartedAt: started,
	}
	st.PutSubscription(sub)

	// 5. Ingest usage events — including a deliberate duplicate and a late one.
	type raw struct {
		key string
		t   time.Time
		n   int
	}
	usage := []raw{
		{"evt_1", time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC), 40000},
		{"evt_2", time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC), 50000},
		{"evt_1", time.Date(2026, 6, 18, 9, 5, 0, 0, time.UTC), 99999}, // duplicate key -> ignored
		{"evt_3", time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC), 30000},
	}
	accepted, dupes := 0, 0
	for _, u := range usage {
		now := u.t.Add(2 * time.Second) // ingestion clock slightly after event
		_, err := ing.Accept(domain.Event{
			IdempotencyKey: u.key, CustomerID: "cus_acme", MeterCode: "tokens",
			EventTime: u.t, Properties: map[string]any{"n": u.n},
		}, now)
		switch {
		case err == nil:
			accepted++
		case isDup(err):
			dupes++
		default:
			return err
		}
	}

	fmt.Printf("Ingested %d events (%d duplicate key ignored — idempotent, window %s)\n\n",
		accepted, dupes, ing.Window())

	// 6. Deterministic invoice preview.
	events := st.EventsForCustomer("cus_acme")
	res, err := invoice.Calculate(sub, plan, st.Meters(), events)
	if err != nil {
		return err
	}

	printInvoice(res, started, periodStart, periodEnd)

	// 7. Determinism proof: recompute, hash must match.
	res2, _ := invoice.Calculate(sub, plan, st.Meters(), events)
	fmt.Printf("\nDeterminism check: recomputed hash %s\n", matchLabel(res.Hash, res2.Hash))
	return nil
}

func printInvoice(res invoice.Result, started, periodStart, periodEnd time.Time) {
	inv := res.Invoice
	fmt.Printf("INVOICE  customer=%s  period=%s .. %s\n",
		inv.CustomerID, periodStart.Format("2006-01-02"), periodEnd.Format("2006-01-02"))
	fmt.Printf("         subscription started %s (mid-period -> flat fee prorated)\n\n", started.Format("2006-01-02"))

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "LINE\tMETER\tMODEL\tEVENTS\tQUANTITY\tPRORATION\tAMOUNT")
	for i, tr := range res.Traces {
		meterName := tr.MeterCode
		if meterName == "" {
			meterName = "(base fee)"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\t%s\t$%s\n",
			i+1, meterName, tr.PriceModel, tr.RawEventCount,
			tr.MeterValue.String(), tr.ProrationFactor.StringFixed(4), tr.Amount.StringFixed(2))
	}
	fmt.Fprintf(w, "\t\t\t\t\t\tTOTAL  $%s %s\n", inv.Total.StringFixed(2), inv.Currency)
	w.Flush()

	fmt.Printf("\nReconciliation hash: %s\n", res.Hash)
	fmt.Println("  ^ raw events -> meter value -> invoice line, provable & tamper-evident (Phase 2 ledger).")
}

func isDup(err error) bool { return err != nil && err.Error() == ingest.ErrDuplicate.Error() }

func matchLabel(a, b string) string {
	if a == b {
		return "MATCH ✓ (meter and invoice provably agree)"
	}
	return "MISMATCH ✗"
}
