package pricing

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

func dq(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func TestPackagePricing(t *testing.T) {
	// $10 per block of 1,000 events, rounded up.
	p := domain.Price{ID: "p1", Model: domain.ModelPackage, Currency: "USD",
		UnitAmount: dq("10.00"), PackageSize: dq("1000")}
	cases := []struct {
		qty, want string
	}{
		{"0", "0.00"},     // no usage, no charge
		{"1", "10.00"},    // 1 unit still needs a whole block
		{"1000", "10.00"}, // exactly one block
		{"1001", "20.00"}, // spills into a second block
		{"2500", "30.00"}, // 3 blocks
	}
	for _, c := range cases {
		got, err := Calculate(p, dq(c.qty))
		if err != nil {
			t.Fatalf("qty %s: %v", c.qty, err)
		}
		if got.Amount() != c.want {
			t.Errorf("package qty %s = %s, want %s", c.qty, got.Amount(), c.want)
		}
	}
}

func TestPackageRequiresPositiveSize(t *testing.T) {
	p := domain.Price{ID: "p1", Model: domain.ModelPackage, Currency: "USD", UnitAmount: dq("10")}
	if _, err := Calculate(p, dq("100")); err == nil {
		t.Fatal("expected error when package_size is zero")
	}
}

func TestPercentageCommission(t *testing.T) {
	// 2.5% of the metered monetary base (e.g. a marketplace's GMV).
	p := domain.Price{ID: "p1", Model: domain.ModelPercentage, Currency: "USD", Percentage: dq("2.5")}
	got, err := Calculate(p, dq("1000.00"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount() != "25.00" {
		t.Fatalf("2.5%% of 1000 = %s, want 25.00", got.Amount())
	}
}

func TestMinimumSpendFloorsTheCharge(t *testing.T) {
	// Per-unit usage with a $50 minimum spend (commitment): below the floor bills $50.
	p := domain.Price{ID: "p1", Model: domain.ModelPerUnit, Currency: "USD",
		UnitAmount: dq("0.01"), MinimumAmount: dq("50.00")}
	low, _ := Calculate(p, dq("100")) // 100 * 0.01 = $1 -> floored to $50
	if low.Amount() != "50.00" {
		t.Fatalf("below minimum = %s, want 50.00", low.Amount())
	}
	high, _ := Calculate(p, dq("10000")) // 10000 * 0.01 = $100 -> above the floor, unchanged
	if high.Amount() != "100.00" {
		t.Fatalf("above minimum = %s, want 100.00", high.Amount())
	}
}

func TestMaximumCapsTheCharge(t *testing.T) {
	// A percentage commission capped at $100 per period.
	p := domain.Price{ID: "p1", Model: domain.ModelPercentage, Currency: "USD",
		Percentage: dq("10"), MaximumAmount: dq("100.00")}
	got, _ := Calculate(p, dq("5000")) // 10% of 5000 = $500 -> capped at $100
	if got.Amount() != "100.00" {
		t.Fatalf("capped charge = %s, want 100.00", got.Amount())
	}
}

func TestDeterministicAcrossModels(t *testing.T) {
	// Same inputs, same output — the property the reconciliation ledger relies on.
	p := domain.Price{ID: "p1", Model: domain.ModelPackage, Currency: "USD",
		UnitAmount: dq("10"), PackageSize: dq("1000"), MinimumAmount: dq("5")}
	a, _ := Calculate(p, dq("2500"))
	b, _ := Calculate(p, dq("2500"))
	if a.Amount() != b.Amount() {
		t.Fatalf("non-deterministic: %s vs %s", a.Amount(), b.Amount())
	}
}
