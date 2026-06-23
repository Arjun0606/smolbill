package engine

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

func dg(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func gateStore(t *testing.T) *memory.Store {
	t.Helper()
	st := memory.New()
	_ = st.PutCustomer(domain.Customer{ID: "cus_1", Name: "Acme"})
	_ = st.PutMeter(domain.Meter{Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"})
	// 1,000-token metered entitlement for the period.
	_ = st.PutEntitlement(domain.Entitlement{
		ID: "ent_1", CustomerID: "cus_1", Feature: "api", Kind: domain.EntMetered,
		MeterCode: "tokens", LimitValue: dg("1000"),
		PeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	})
	return st
}

func ingest(t *testing.T, st *memory.Store, key string, n int) {
	t.Helper()
	_ = st.AppendEvent(domain.Event{
		IdempotencyKey: key, CustomerID: "cus_1", MeterCode: "tokens",
		EventTime:  time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		Properties: map[string]any{"n": n},
	})
}

func TestGateAllowsWithinLimit(t *testing.T) {
	st := gateStore(t)
	ingest(t, st, "e1", 400) // 400 of 1000 used
	d, err := Gate(st, GateRequest{CustomerID: "cus_1", Feature: "api", Quantity: dg("100")})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("400 + 100 <= 1000 should allow, got DENIED: %s", d.Reason)
	}
}

func TestGateDeniesAtLimit(t *testing.T) {
	st := gateStore(t)
	ingest(t, st, "e1", 950) // 950 used
	d, err := Gate(st, GateRequest{CustomerID: "cus_1", Feature: "api", Quantity: dg("100")})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("950 + 100 > 1000 must be DENIED")
	}
	if d.Remaining != "50" {
		t.Fatalf("remaining = %s, want 50", d.Remaining)
	}
}

func TestGateDeniesUnknownFeature(t *testing.T) {
	st := gateStore(t)
	d, _ := Gate(st, GateRequest{CustomerID: "cus_1", Feature: "premium"})
	if d.Allowed {
		t.Fatal("a feature with no entitlement must be denied by default")
	}
}

func TestGateWalletBalanceBlock(t *testing.T) {
	st := gateStore(t)
	_, _ = st.TopUpWallet("cus_1", dg("5.00"), "USD", "seed", "w1")
	// Cost within balance -> allow.
	if d, _ := Gate(st, GateRequest{CustomerID: "cus_1", Cost: dg("3.00"), Currency: "USD"}); !d.Allowed {
		t.Fatalf("cost 3 <= balance 5 should allow, got %s", d.Reason)
	}
	// Cost over balance -> hard block at insufficient funds.
	if d, _ := Gate(st, GateRequest{CustomerID: "cus_1", Cost: dg("9.00"), Currency: "USD"}); d.Allowed {
		t.Fatal("cost 9 > balance 5 must be DENIED")
	}
}

func TestGateRequiresSomething(t *testing.T) {
	st := gateStore(t)
	if _, err := Gate(st, GateRequest{CustomerID: "cus_1"}); err == nil {
		t.Fatal("gate with neither feature nor cost should error")
	}
}
