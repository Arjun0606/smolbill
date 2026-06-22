package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

func dec(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

// seed builds a store with one customer on a base plan (flat $49 + $0.001/token)
// who has used 15,000 tokens this period → a live bill of $64.00.
func seed(t *testing.T) *memory.Store {
	t.Helper()
	st := memory.New()
	ps := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pe := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutCustomer(domain.Customer{ID: "cus_1"}))
	must(st.PutMeter(domain.Meter{Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"}))
	must(st.PutPlan(domain.Plan{ID: "plan_1", Name: "base", Version: 1, Prices: []domain.Price{
		{ID: "p_base", Model: domain.ModelFlat, Currency: "USD", FlatAmount: dec("49.00")},
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: dec("0.001")},
	}}))
	must(st.PutSubscription(domain.Subscription{
		ID: "sub_1", CustomerID: "cus_1", PlanID: "plan_1", PlanVersion: 1,
		Status: domain.SubActive, CurrentPeriodStart: ps, CurrentPeriodEnd: pe, StartedAt: ps,
	}))
	for i, n := range []int{10000, 5000} {
		must(st.AppendEvent(domain.Event{
			ID: "evt_" + string(rune('a'+i)), CustomerID: "cus_1", MeterCode: "tokens",
			EventTime: ps.AddDate(0, 0, 5+i), Properties: map[string]any{"n": float64(n)},
		}))
	}
	return st
}

func TestSimulatePlanChange(t *testing.T) {
	st := seed(t)

	// Proposed: double the per-unit rate ($0.001 → $0.002).
	// current  = 49 + 15000*0.001 = 64.00
	// proposed = 49 + 15000*0.002 = 79.00  → delta +15.00
	proposed := PlanInput{Name: "base", Version: 1, Prices: []PriceInput{
		{Model: "flat", Currency: "USD", FlatAmount: "49.00"},
		{MeterCode: "tokens", Model: "per_unit", Currency: "USD", UnitAmount: "0.002"},
	}}

	res, err := SimulatePlanChange(st, "cus_1", proposed)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CurrentTotal.Equal(dec("64.00")) {
		t.Errorf("current total = %s, want 64.00", res.CurrentTotal)
	}
	if !res.ProposedTotal.Equal(dec("79.00")) {
		t.Errorf("proposed total = %s, want 79.00", res.ProposedTotal)
	}
	if !res.Delta.Equal(dec("15.00")) {
		t.Errorf("delta = %s, want 15.00", res.Delta)
	}

	// The token line itself should show the +$15 swing.
	var found bool
	for _, l := range res.Lines {
		if l.MeterCode == "tokens" {
			found = true
			if !l.CurrentAmount.Equal(dec("15.00")) || !l.ProposedAmount.Equal(dec("30.00")) {
				t.Errorf("tokens line: now %s, proposed %s; want 15.00 / 30.00", l.CurrentAmount, l.ProposedAmount)
			}
		}
	}
	if !found {
		t.Error("expected a tokens line delta")
	}

	// THE SANDBOX GUARANTEE: simulating must not have touched live state — the
	// real bill must still be $64.00.
	live, _, err := ComputeForActiveSub(st, "cus_1")
	if err != nil {
		t.Fatal(err)
	}
	if !live.Invoice.Total.Equal(dec("64.00")) {
		t.Fatalf("simulate persisted state: live bill changed to %s — must stay 64.00", live.Invoice.Total)
	}
}

func TestComputeDeterministic(t *testing.T) {
	st := seed(t)
	a, _, err := ComputeForActiveSub(st, "cus_1")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ComputeForActiveSub(st, "cus_1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatalf("non-deterministic hash: %s != %s", a.Hash, b.Hash)
	}
	if !a.Invoice.Total.Equal(b.Invoice.Total) {
		t.Fatalf("totals differ across identical computes: %s vs %s", a.Invoice.Total, b.Invoice.Total)
	}
}

func TestSimulateNoActiveSub(t *testing.T) {
	st := memory.New()
	if err := st.PutCustomer(domain.Customer{ID: "cus_x"}); err != nil {
		t.Fatal(err)
	}
	_, err := SimulatePlanChange(st, "cus_x", PlanInput{
		Name:   "x",
		Prices: []PriceInput{{Model: "flat", Currency: "USD", FlatAmount: "1.00"}},
	})
	if !errors.Is(err, ErrNoActiveSub) {
		t.Fatalf("want ErrNoActiveSub, got %v", err)
	}
}

func TestBuildPlanRejectsBadTier(t *testing.T) {
	_, err := BuildPlan(PlanInput{Name: "p", Prices: []PriceInput{{
		Model: "tiered_graduated", Currency: "USD",
		Tiers: []TierInput{{UpTo: ptr("not-a-number"), UnitAmount: "0.01"}},
	}}})
	if err == nil {
		t.Fatal("expected an error for a malformed tier up_to")
	}
}

func ptr(s string) *string { return &s }
