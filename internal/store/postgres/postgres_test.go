package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// These tests require a real Postgres. Set SMOLBILL_TEST_DATABASE_URL to run;
// otherwise they skip (so `go test ./...` stays green without a DB).
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("SMOLBILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SMOLBILL_TEST_DATABASE_URL to run Postgres integration tests")
	}
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	// Clean slate for repeatable runs.
	for _, tbl := range []string{"wallet_transactions", "wallets", "alerts", "reconciliation_ledger", "invoice_lines", "invoices", "entitlements", "events", "prices", "subscriptions", "plans", "meters", "customers"} {
		if _, err := st.pool.Exec(context.Background(), "DELETE FROM "+tbl); err != nil {
			t.Fatalf("cleanup %s: %v", tbl, err)
		}
	}
	return st
}

func d(s string) decimal.Decimal   { v, _ := decimal.NewFromString(s); return v }
func dp(s string) *decimal.Decimal { v := d(s); return &v }

func TestPostgresRoundTrip(t *testing.T) {
	st := testStore(t)

	if err := st.PutCustomer(domain.Customer{ID: "cus_t", Name: "T", ExternalID: "ext"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetCustomer("cus_t")
	if err != nil || !ok {
		t.Fatalf("GetCustomer ok=%v err=%v", ok, err)
	}
	if got.Name != "T" || got.ExternalID != "ext" {
		t.Fatalf("customer round-trip mismatch: %+v", got)
	}

	if err := st.PutMeter(domain.Meter{Code: "tokens", Name: "Tokens", Aggregation: domain.AggSum, PropertyKey: "n"}); err != nil {
		t.Fatal(err)
	}

	plan := domain.Plan{ID: "plan_t", Name: "Pro", Version: 1, Prices: []domain.Price{
		{Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("49.00")},
		{MeterCode: "tokens", Model: domain.ModelTieredGraduated, Currency: "USD", Tiers: []domain.Tier{
			{UpTo: dp("1000"), UnitAmount: d("0.05")},
			{UpTo: nil, UnitAmount: d("0.01")},
		}},
	}}
	if err := st.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	gotPlan, ok, err := st.GetPlan("plan_t")
	if err != nil || !ok {
		t.Fatalf("GetPlan ok=%v err=%v", ok, err)
	}
	if len(gotPlan.Prices) != 2 {
		t.Fatalf("prices = %d, want 2", len(gotPlan.Prices))
	}
	// Verify the tiered price's decimals and tiers survived JSONB round-trip.
	var tiered domain.Price
	for _, p := range gotPlan.Prices {
		if p.Model == domain.ModelTieredGraduated {
			tiered = p
		}
	}
	if len(tiered.Tiers) != 2 || !tiered.Tiers[0].UnitAmount.Equal(d("0.05")) {
		t.Fatalf("tiers did not round-trip: %+v", tiered.Tiers)
	}
	if tiered.Tiers[1].UpTo != nil {
		t.Fatal("final tier UpTo should be nil after round-trip")
	}
}

func TestPostgresIdempotentIngest(t *testing.T) {
	st := testStore(t)
	if err := st.PutCustomer(domain.Customer{ID: "cus_i", Name: "I"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMeter(domain.Meter{Code: "tokens", Name: "Tokens", Aggregation: domain.AggSum, PropertyKey: "n"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	e := domain.Event{IdempotencyKey: "k1", CustomerID: "cus_i", MeterCode: "tokens",
		EventTime: now, Properties: map[string]any{"n": 100}, IngestedAt: now}

	seen, err := st.SeenKey("k1", now, time.Hour)
	if err != nil || seen {
		t.Fatalf("SeenKey before insert = %v, err %v", seen, err)
	}
	if err := st.AppendEvent(e); err != nil {
		t.Fatal(err)
	}
	seen, _ = st.SeenKey("k1", now, time.Hour)
	if !seen {
		t.Fatal("SeenKey after insert should be true")
	}
	evs, err := st.EventsForCustomer("cus_i")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Properties["n"] == nil {
		t.Fatalf("event/properties did not round-trip: %+v", evs)
	}
}

func TestPostgresFinalizeAndLedger(t *testing.T) {
	st := testStore(t)
	if err := st.PutCustomer(domain.Customer{ID: "cus_f", Name: "F"}); err != nil {
		t.Fatal(err)
	}
	// Invoices FK to subscriptions, which FK to plans — set them up first.
	if err := st.PutPlan(domain.Plan{ID: "plan_f", Name: "F", Version: 1, Prices: []domain.Price{
		{Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("49.00")},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSubscription(domain.Subscription{
		ID: "sub_f", CustomerID: "cus_f", PlanID: "plan_f", PlanVersion: 1, Status: domain.SubActive,
		CurrentPeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		CurrentPeriodEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	inv := domain.Invoice{
		ID: "inv_f", CustomerID: "cus_f", SubscriptionID: "sub_f",
		PeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Status:      "finalized", Total: d("64.00"), Currency: "USD",
		Lines: []domain.InvoiceLine{
			{MeterCode: "tokens", Quantity: d("15000"), UnitPrice: d("0.001"), Amount: d("15.00")},
			{MeterCode: "", Quantity: d("1"), UnitPrice: d("49.00"), Amount: d("49.00")},
		},
	}
	ledger := []domain.LedgerRow{
		{MeterCode: "tokens", RawEventCount: 2, MeterValue: d("15000"), InvoiceLineAmount: d("15.00"), ComputedHash: "abc123"},
		{MeterCode: "", RawEventCount: 0, MeterValue: d("1"), InvoiceLineAmount: d("49.00"), ComputedHash: "abc123"},
	}
	if err := st.SaveFinalizedInvoice(inv, ledger); err != nil {
		t.Fatal(err)
	}

	got, ok, err := st.GetInvoice("inv_f")
	if err != nil || !ok {
		t.Fatalf("GetInvoice ok=%v err=%v", ok, err)
	}
	if !got.Total.Equal(d("64.00")) || len(got.Lines) != 2 {
		t.Fatalf("invoice round-trip wrong: total=%s lines=%d", got.Total, len(got.Lines))
	}

	rows, err := st.GetLedger("inv_f")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2", len(rows))
	}
	if rows[0].ComputedHash != "abc123" {
		t.Fatalf("ledger hash not persisted: %+v", rows[0])
	}
	// meter_code MUST round-trip — reconcile joins ledger to live lines by it.
	byMeter := map[string]domain.LedgerRow{}
	for _, r := range rows {
		byMeter[r.MeterCode] = r
	}
	if _, ok := byMeter["tokens"]; !ok {
		t.Fatalf("ledger lost meter_code; rows=%+v", rows)
	}
	if !byMeter["tokens"].MeterValue.Equal(d("15000")) {
		t.Fatalf("tokens ledger row meter_value = %s, want 15000", byMeter["tokens"].MeterValue)
	}
}

func TestPostgresEntitlementRoundTrip(t *testing.T) {
	st := testStore(t)
	if err := st.PutCustomer(domain.Customer{ID: "cus_e", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	e := domain.Entitlement{
		ID: "ent_1", CustomerID: "cus_e", Feature: "tokens", Kind: domain.EntMetered,
		MeterCode: "tokens", LimitValue: d("10000"),
		PeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := st.PutEntitlement(e); err != nil {
		t.Fatal(err)
	}
	got, err := st.EntitlementsForCustomer("cus_e")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].LimitValue.Equal(d("10000")) || got[0].MeterCode != "tokens" {
		t.Fatalf("entitlement round-trip wrong: %+v", got)
	}
}

func TestPostgresAlertRoundTrip(t *testing.T) {
	st := testStore(t)
	if err := st.PutCustomer(domain.Customer{ID: "cus_al", Name: "AL"}); err != nil {
		t.Fatal(err)
	}
	a := domain.Alert{
		ID: "alert_1", CustomerID: "cus_al", Budget: d("100.00"), Currency: "USD",
		Thresholds: []int{50, 80, 100}, WebhookURL: "https://hook.test/x",
	}
	if err := st.PutAlert(a); err != nil {
		t.Fatal(err)
	}
	got, err := st.AlertsForCustomer("cus_al")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Budget.Equal(d("100.00")) || len(got[0].Thresholds) != 3 {
		t.Fatalf("alert round-trip wrong: %+v", got)
	}
	if got[0].MaxFired != 0 {
		t.Fatalf("max_fired = %d, want 0", got[0].MaxFired)
	}
	// Advance the high-water mark.
	if err := st.UpdateAlertFired("alert_1", 80); err != nil {
		t.Fatal(err)
	}
	got, _ = st.AlertsForCustomer("cus_al")
	if got[0].MaxFired != 80 {
		t.Fatalf("max_fired after update = %d, want 80", got[0].MaxFired)
	}
}

func TestPostgresWalletIdempotentTopup(t *testing.T) {
	st := testStore(t)
	if err := st.PutCustomer(domain.Customer{ID: "cus_w", Name: "W"}); err != nil {
		t.Fatal(err)
	}
	w, err := st.TopUpWallet("cus_w", d("50.00"), "USD", "prepaid", "k1")
	if err != nil || !w.Balance.Equal(d("50.00")) {
		t.Fatalf("first topup balance=%s err=%v", w.Balance, err)
	}
	// Same key -> no double credit.
	w, err = st.TopUpWallet("cus_w", d("50.00"), "USD", "prepaid", "k1")
	if err != nil || !w.Balance.Equal(d("50.00")) {
		t.Fatalf("idempotent topup balance=%s err=%v", w.Balance, err)
	}
	// New key -> credits.
	w, _ = st.TopUpWallet("cus_w", d("40.00"), "USD", "topup", "k2")
	if !w.Balance.Equal(d("90.00")) {
		t.Fatalf("second topup balance=%s, want 90.00", w.Balance)
	}
	txns, _ := st.WalletTransactions("cus_w")
	if len(txns) != 2 {
		t.Fatalf("wallet txns = %d, want 2", len(txns))
	}
}
