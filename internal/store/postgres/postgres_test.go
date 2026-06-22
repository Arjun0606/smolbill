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
	for _, tbl := range []string{"events", "prices", "subscriptions", "plans", "meters", "customers"} {
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
