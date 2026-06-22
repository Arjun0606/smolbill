package engine

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/dunning"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/store"
)

// Analytics is an account-wide snapshot: who you have, what they'll be billed,
// what you've finalized, and how recovery is going. Money fields are bucketed by
// currency (so mixed-currency accounts never get an invalid cross-currency sum)
// and formatted to each currency's minor unit. Computed live from the event log
// and the stores — the same deterministic engine, never a cached counter.
type Analytics struct {
	GeneratedAt         time.Time         `json:"generated_at"`
	Customers           int               `json:"customers"`
	ActiveSubscriptions int               `json:"active_subscriptions"`
	FinalizedInvoices   int               `json:"finalized_invoices"`
	ProjectedRevenue    map[string]string `json:"projected_revenue"` // currency -> this period's projected bill total
	FinalizedRevenue    map[string]string `json:"finalized_revenue"` // currency -> sum of finalized invoice totals
	Dunning             DunningAnalytics  `json:"dunning"`
}

// DunningAnalytics summarizes failed-payment recovery — the visibility Stripe
// gates behind paid Sigma and Baremetrics sells as a whole product.
type DunningAnalytics struct {
	Scheduled       int               `json:"scheduled"`
	Retrying        int               `json:"retrying"`
	RequiresAction  int               `json:"requires_action"`
	Recovered       int               `json:"recovered"`        // collections that ended paid
	Uncollectible   int               `json:"uncollectible"`    // collections written off
	AmountAtRisk    map[string]string `json:"amount_at_risk"`   // unpaid (scheduled+retrying+requires_action)
	AmountRecovered map[string]string `json:"amount_recovered"` // paid collections
	RecoveryRate    string            `json:"recovery_rate"`    // recovered / (recovered+uncollectible), as a percent
}

// ComputeAnalytics builds the account snapshot. It iterates customers (computing
// each active subscription's projected bill through the same engine that
// finalizes invoices) and folds in finalized invoices and collection state.
func ComputeAnalytics(st store.Store, now time.Time) (Analytics, error) {
	a := Analytics{
		GeneratedAt:      now,
		ProjectedRevenue: map[string]string{},
		FinalizedRevenue: map[string]string{},
	}
	projected := map[string]decimal.Decimal{}
	finalized := map[string]decimal.Decimal{}

	customers, err := st.ListCustomers()
	if err != nil {
		return Analytics{}, err
	}
	a.Customers = len(customers)

	for _, c := range customers {
		// Projected revenue: every active subscription's current-period bill.
		subs, err := st.SubscriptionsForCustomer(c.ID)
		if err != nil {
			return Analytics{}, err
		}
		for _, sub := range subs {
			if sub.Status != domain.SubActive {
				continue
			}
			a.ActiveSubscriptions++
			res, err := Compute(st, sub)
			if err != nil {
				// A broken plan shouldn't sink the whole report; skip this sub.
				continue
			}
			projected[res.Invoice.Currency] = projected[res.Invoice.Currency].Add(res.Invoice.Total)
		}
		// Finalized revenue: sum of materialized invoices.
		invs, err := st.InvoicesForCustomer(c.ID)
		if err != nil {
			return Analytics{}, err
		}
		for _, inv := range invs {
			a.FinalizedInvoices++
			finalized[inv.Currency] = finalized[inv.Currency].Add(inv.Total)
		}
	}

	cols, err := st.Collections()
	if err != nil {
		return Analytics{}, err
	}
	atRisk := map[string]int64{}
	recovered := map[string]int64{}
	for _, col := range cols {
		switch dunning.Status(col.Status) {
		case dunning.Scheduled:
			a.Dunning.Scheduled++
			atRisk[col.Currency] += col.AmountMinor
		case dunning.Retrying:
			a.Dunning.Retrying++
			atRisk[col.Currency] += col.AmountMinor
		case dunning.RequiresAction:
			a.Dunning.RequiresAction++
			atRisk[col.Currency] += col.AmountMinor
		case dunning.Paid:
			a.Dunning.Recovered++
			recovered[col.Currency] += col.AmountMinor
		case dunning.Uncollectible:
			a.Dunning.Uncollectible++
		}
	}

	a.Dunning.AmountAtRisk = formatMinorBuckets(atRisk)
	a.Dunning.AmountRecovered = formatMinorBuckets(recovered)
	a.Dunning.RecoveryRate = recoveryRate(a.Dunning.Recovered, a.Dunning.Uncollectible)

	for cur, d := range projected {
		a.ProjectedRevenue[cur] = money.Format(d, cur)
	}
	for cur, d := range finalized {
		a.FinalizedRevenue[cur] = money.Format(d, cur)
	}
	return a, nil
}

// recoveryRate is recovered / (recovered + uncollectible) as a percent string,
// among collections that have reached a terminal outcome. "n/a" before any
// resolve, so we never imply 0% recovery when nothing has failed yet.
func recoveryRate(recovered, uncollectible int) string {
	resolved := recovered + uncollectible
	if resolved == 0 {
		return "n/a"
	}
	pct := decimal.NewFromInt(int64(recovered)).
		Mul(decimal.NewFromInt(100)).
		Div(decimal.NewFromInt(int64(resolved)))
	return pct.StringFixed(1) + "%"
}

func formatMinorBuckets(buckets map[string]int64) map[string]string {
	out := map[string]string{}
	for cur, minor := range buckets {
		out[cur] = money.FromMinor(minor, cur).Amount()
	}
	return out
}
