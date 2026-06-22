package engine

import (
	"testing"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

func ptrTime(t time.Time) *time.Time { return &t }

// TestComputeAnalytics builds an account with one customer billed $64 this period,
// one finalized $30 invoice, and three collections (one at-risk, one recovered,
// one written off) and asserts the rolled-up snapshot.
func TestComputeAnalytics(t *testing.T) {
	st := seed(t) // from engine_test.go: cus_1, active sub, projected $64.00
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)

	// A finalized $30 invoice.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SaveFinalizedInvoice(domain.Invoice{
		ID: "inv_done", CustomerID: "cus_1", Total: dec("30.00"), Currency: "USD", Status: "finalized",
	}, nil))

	// Three collections: $10 retrying (at risk), $20 recovered (paid), $5 written off.
	must(st.PutCollection(domain.Collection{InvoiceID: "inv_a", Status: "retrying", Currency: "USD", AmountMinor: 1000, NextAttemptAt: ptrTime(now)}))
	must(st.PutCollection(domain.Collection{InvoiceID: "inv_b", Status: "paid", Currency: "USD", AmountMinor: 2000}))
	must(st.PutCollection(domain.Collection{InvoiceID: "inv_c", Status: "uncollectible", Currency: "USD", AmountMinor: 500}))

	a, err := ComputeAnalytics(st, now)
	if err != nil {
		t.Fatal(err)
	}

	if a.Customers != 1 || a.ActiveSubscriptions != 1 {
		t.Errorf("customers=%d subs=%d, want 1/1", a.Customers, a.ActiveSubscriptions)
	}
	if a.ProjectedRevenue["USD"] != "64.00" {
		t.Errorf("projected USD = %q, want 64.00", a.ProjectedRevenue["USD"])
	}
	if a.FinalizedInvoices != 1 || a.FinalizedRevenue["USD"] != "30.00" {
		t.Errorf("finalized: %d invoices, %q USD; want 1, 30.00", a.FinalizedInvoices, a.FinalizedRevenue["USD"])
	}
	if a.Dunning.Retrying != 1 || a.Dunning.Recovered != 1 || a.Dunning.Uncollectible != 1 {
		t.Errorf("dunning counts = %+v", a.Dunning)
	}
	if a.Dunning.AmountAtRisk["USD"] != "10.00" {
		t.Errorf("at risk USD = %q, want 10.00", a.Dunning.AmountAtRisk["USD"])
	}
	if a.Dunning.AmountRecovered["USD"] != "20.00" {
		t.Errorf("recovered USD = %q, want 20.00", a.Dunning.AmountRecovered["USD"])
	}
	// recovered 1 / (recovered 1 + uncollectible 1) = 50.0%
	if a.Dunning.RecoveryRate != "50.0%" {
		t.Errorf("recovery rate = %q, want 50.0%%", a.Dunning.RecoveryRate)
	}
}

func TestRecoveryRateNAWhenNoneResolved(t *testing.T) {
	if got := recoveryRate(0, 0); got != "n/a" {
		t.Errorf("recoveryRate(0,0) = %q, want n/a", got)
	}
	if got := recoveryRate(3, 1); got != "75.0%" {
		t.Errorf("recoveryRate(3,1) = %q, want 75.0%%", got)
	}
}

// TestAnalyticsEmptyAccount proves a fresh account reports zeros cleanly, not a crash.
func TestAnalyticsEmptyAccount(t *testing.T) {
	a, err := ComputeAnalytics(memory.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.Customers != 0 || a.Dunning.RecoveryRate != "n/a" {
		t.Errorf("empty account snapshot wrong: %+v", a)
	}
}
