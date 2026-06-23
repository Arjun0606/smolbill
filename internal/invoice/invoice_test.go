package invoice

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

func d(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}
func dp(s string) *decimal.Decimal { v := d(s); return &v }

var (
	periodStart = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd   = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) // 30 days
)

func tokensMeter() map[string]domain.Meter {
	return map[string]domain.Meter{
		"tokens": {Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"},
	}
}

func sub(plan domain.Plan, startedAt time.Time) domain.Subscription {
	return domain.Subscription{
		ID: "sub_1", CustomerID: "cus_1", PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: periodStart, CurrentPeriodEnd: periodEnd,
		StartedAt: startedAt,
	}
}

func evt(t time.Time, n int) domain.Event {
	return domain.Event{MeterCode: "tokens", EventTime: t, Properties: map[string]any{"n": float64(n)}}
}

func TestFlatPlusUsageFullPeriod(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_base", Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("49.00")},
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	events := []domain.Event{
		evt(time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), 10000),
		evt(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 5000),
	}
	res, err := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	if err != nil {
		t.Fatal(err)
	}
	// base 49.00 + 15000 * 0.001 = 49 + 15 = 64.00
	if !res.Invoice.Total.Equal(d("64.00")) {
		t.Fatalf("total = %s, want 64.00", res.Invoice.Total)
	}
}

func TestTaxLine(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_base", Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("100.00")},
	}}
	// 8.25% tax on a $100 flat fee → $8.25 tax line, total $108.25.
	res, err := Calculate(sub(plan, periodStart), plan, tokensMeter(), nil, d("8.25"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Invoice.Total.Equal(d("108.25")) {
		t.Fatalf("total = %s, want 108.25", res.Invoice.Total)
	}
	var tax *domain.InvoiceLine
	for i := range res.Invoice.Lines {
		if res.Invoice.Lines[i].MeterCode == TaxLineCode {
			tax = &res.Invoice.Lines[i]
		}
	}
	if tax == nil {
		t.Fatal("no tax line in the invoice")
	}
	if !tax.Amount.Equal(d("8.25")) {
		t.Fatalf("tax line = %s, want 8.25", tax.Amount)
	}
	// Deterministic: same inputs → same hash; and tax changes the hash.
	res2, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), nil, d("8.25"))
	if res.Hash != res2.Hash {
		t.Fatal("taxed invoice must be deterministic")
	}
	if noTax, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), nil); noTax.Hash == res.Hash {
		t.Fatal("a tax line must change the verification hash")
	}
}

func TestFlatProratedMidPeriodStart(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_base", Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("30.00")},
	}}
	// Start exactly halfway: 2026-06-16 00:00 -> 15 of 30 days active.
	started := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	res, err := Calculate(sub(plan, started), plan, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 30.00 * (15/30) = 15.00
	if !res.Invoice.Total.Equal(d("15.00")) {
		t.Fatalf("prorated total = %s, want 15.00", res.Invoice.Total)
	}
}

func TestUsageNotProrated(t *testing.T) {
	// Even when the sub starts late, usage is charged in full (you used it).
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	started := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	events := []domain.Event{evt(time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), 10000)}
	res, _ := Calculate(sub(plan, started), plan, tokensMeter(), events)
	// 10000 * 0.001 = 10.00, no proration
	if !res.Invoice.Total.Equal(d("10.00")) {
		t.Fatalf("usage total = %s, want 10.00", res.Invoice.Total)
	}
}

func TestUnderBillRounding(t *testing.T) {
	// 0.001 * 12345 = 12.345 -> must round DOWN to 12.34, never 12.35.
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	events := []domain.Event{evt(periodStart.Add(time.Hour), 12345)}
	res, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	if !res.Invoice.Total.Equal(d("12.34")) {
		t.Fatalf("under-bill rounding = %s, want 12.34", res.Invoice.Total)
	}
}

func TestDeterministicHashStable(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_base", Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("49.00")},
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	events := []domain.Event{evt(periodStart.Add(time.Hour), 1000)}
	r1, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	r2, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	if r1.Hash == "" || r1.Hash != r2.Hash {
		t.Fatalf("hash not stable: %q vs %q", r1.Hash, r2.Hash)
	}
}

func TestHashChangesWithUsage(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	r1, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), []domain.Event{evt(periodStart.Add(time.Hour), 1000)})
	r2, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), []domain.Event{evt(periodStart.Add(time.Hour), 2000)})
	if r1.Hash == r2.Hash {
		t.Fatal("hash must change when usage changes")
	}
}

func TestPlanMismatchErrors(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 2}
	s := sub(plan, periodStart)
	s.PlanVersion = 1 // mismatch
	if _, err := Calculate(s, plan, nil, nil); err == nil {
		t.Fatal("expected plan/version mismatch error")
	}
}

func TestTraceCarriesRawEventCount(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	events := []domain.Event{
		evt(periodStart.Add(time.Hour), 100),
		evt(periodStart.Add(2*time.Hour), 200),
		evt(periodStart.Add(3*time.Hour), 300),
	}
	res, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	if len(res.Traces) != 1 {
		t.Fatalf("traces = %d, want 1", len(res.Traces))
	}
	if res.Traces[0].RawEventCount != 3 {
		t.Fatalf("raw event count = %d, want 3", res.Traces[0].RawEventCount)
	}
	if !res.Traces[0].MeterValue.Equal(d("600")) {
		t.Fatalf("meter value = %s, want 600", res.Traces[0].MeterValue)
	}
}

func TestGraduatedTieredInvoice(t *testing.T) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p_tok", MeterCode: "tokens", Model: domain.ModelTieredGraduated, Currency: "USD", Tiers: []domain.Tier{
			{UpTo: dp("1000"), UnitAmount: d("0.05")},
			{UpTo: nil, UnitAmount: d("0.01")},
		}},
	}}
	events := []domain.Event{evt(periodStart.Add(time.Hour), 3000)}
	res, _ := Calculate(sub(plan, periodStart), plan, tokensMeter(), events)
	// 1000*0.05 + 2000*0.01 = 50 + 20 = 70
	if !res.Invoice.Total.Equal(d("70.00")) {
		t.Fatalf("graduated total = %s, want 70.00", res.Invoice.Total)
	}
}
