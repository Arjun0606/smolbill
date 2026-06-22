package reconcile

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/invoice"
)

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

var (
	pStart = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pEnd   = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
)

// tokensMeter / a per-unit plan at $0.001, used by the helpers below.
func setup() (domain.Subscription, domain.Plan, map[string]domain.Meter) {
	plan := domain.Plan{ID: "plan_1", Version: 1, Prices: []domain.Price{
		{ID: "p", MeterCode: "tokens", Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.001")},
	}}
	sub := domain.Subscription{
		ID: "sub_1", CustomerID: "cus_1", PlanID: "plan_1", PlanVersion: 1,
		Status: domain.SubActive, CurrentPeriodStart: pStart, CurrentPeriodEnd: pEnd, StartedAt: pStart,
	}
	meters := map[string]domain.Meter{"tokens": {Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"}}
	return sub, plan, meters
}

func evt(key string, n int) domain.Event {
	return domain.Event{IdempotencyKey: key, MeterCode: "tokens", CustomerID: "cus_1",
		EventTime: pStart.Add(time.Hour), Properties: map[string]any{"n": float64(n)}}
}

// finalize computes an invoice and returns the stored invoice + ledger that
// would have been persisted at finalize time.
func finalize(sub domain.Subscription, plan domain.Plan, meters map[string]domain.Meter, events []domain.Event) (domain.Invoice, []domain.LedgerRow) {
	res, err := invoice.Calculate(sub, plan, meters, events)
	if err != nil {
		panic(err)
	}
	inv := res.Invoice
	inv.ID = "inv_1"
	inv.Status = "finalized"
	return inv, LedgerFromResult("inv_1", res)
}

func TestConsistentWhenNothingChanged(t *testing.T) {
	sub, plan, meters := setup()
	events := []domain.Event{evt("e1", 1000), evt("e2", 2000)} // 3000 -> $3.00

	storedInv, ledger := finalize(sub, plan, meters, events)
	live, _ := invoice.Calculate(sub, plan, meters, events) // same events
	p := Build(storedInv, ledger, live)

	if !p.Consistent || p.Verdict != "consistent" {
		t.Fatalf("expected consistent, got verdict=%q diffs=%v lines=%+v", p.Verdict, p.Diffs, p.Lines)
	}
	if !p.HashMatch {
		t.Fatal("hash should match when events unchanged")
	}
	if p.StoredTotal != "3.00" || p.LiveTotal != "3.00" {
		t.Fatalf("totals = %s / %s, want 3.00", p.StoredTotal, p.LiveTotal)
	}
}

func TestDriftWhenLateEventArrives(t *testing.T) {
	sub, plan, meters := setup()
	atFinalize := []domain.Event{evt("e1", 1000), evt("e2", 2000)} // 3000 -> $3.00
	storedInv, ledger := finalize(sub, plan, meters, atFinalize)

	// A late event arrives after finalize.
	withLate := append(append([]domain.Event{}, atFinalize...), evt("e3_late", 5000)) // +5000 -> 8000 -> $8.00
	live, _ := invoice.Calculate(sub, plan, meters, withLate)

	p := Build(storedInv, ledger, live)
	if p.Consistent {
		t.Fatal("expected drift_detected after late event")
	}
	if p.Verdict != "drift_detected" {
		t.Fatalf("verdict = %q, want drift_detected", p.Verdict)
	}
	if p.HashMatch {
		t.Fatal("hash must differ after late event")
	}
	if len(p.Diffs) == 0 {
		t.Fatal("expected invoice-level total diff")
	}
	// The line for tokens must show the raw-count and amount change.
	var line LineProof
	for _, l := range p.Lines {
		if l.MeterCode == "tokens" {
			line = l
		}
	}
	if line.StoredRawEventCount != 2 || line.LiveRawEventCount != 3 {
		t.Fatalf("raw counts = %d -> %d, want 2 -> 3", line.StoredRawEventCount, line.LiveRawEventCount)
	}
	if line.StoredAmount != "3.00" || line.LiveAmount != "8.00" {
		t.Fatalf("amounts = %s -> %s, want 3.00 -> 8.00", line.StoredAmount, line.LiveAmount)
	}
	if line.Consistent {
		t.Fatal("token line should be inconsistent")
	}
}

func TestLedgerFromResultCapturesEachLine(t *testing.T) {
	sub, plan, meters := setup()
	res, _ := invoice.Calculate(sub, plan, meters, []domain.Event{evt("e1", 1000)})
	rows := LedgerFromResult("inv_x", res)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].InvoiceID != "inv_x" || rows[0].ComputedHash != res.Hash {
		t.Fatalf("ledger row not linked to invoice/hash: %+v", rows[0])
	}
	if rows[0].RawEventCount != 1 || !rows[0].MeterValue.Equal(d("1000")) {
		t.Fatalf("ledger row stored wrong meter trace: %+v", rows[0])
	}
}
