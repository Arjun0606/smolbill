package pricing

import (
	"testing"

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

// assertAmount checks the full-precision (unrounded) value of a price result.
func assertAmount(t *testing.T, got interface{ Decimal() decimal.Decimal }, want string) {
	t.Helper()
	if !got.Decimal().Equal(d(want)) {
		t.Fatalf("amount = %s, want %s", got.Decimal(), want)
	}
}

func TestFlat(t *testing.T) {
	p := domain.Price{Model: domain.ModelFlat, Currency: "USD", FlatAmount: d("49.00")}
	got, err := Calculate(p, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	assertAmount(t, got, "49.00")
}

func TestPerUnit(t *testing.T) {
	// $0.002 per token * 1500 tokens = $3.00
	p := domain.Price{Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.002")}
	got, err := Calculate(p, d("1500"))
	if err != nil {
		t.Fatal(err)
	}
	assertAmount(t, got, "3.000")
}

func TestPerUnitFractionalPrecisionKept(t *testing.T) {
	// Full precision must survive; rounding is the invoice layer's job.
	p := domain.Price{Model: domain.ModelPerUnit, Currency: "USD", UnitAmount: d("0.0001")}
	got, _ := Calculate(p, d("12345"))
	assertAmount(t, got, "1.2345")
}

func graduatedPrice() domain.Price {
	// 0-1000 @ $0.05, 1001-5000 @ $0.03, 5001+ @ $0.01
	return domain.Price{
		Model:    domain.ModelTieredGraduated,
		Currency: "USD",
		Tiers: []domain.Tier{
			{UpTo: dp("1000"), UnitAmount: d("0.05")},
			{UpTo: dp("5000"), UnitAmount: d("0.03")},
			{UpTo: nil, UnitAmount: d("0.01")},
		},
	}
}

func TestGraduatedWithinFirstTier(t *testing.T) {
	got, _ := Calculate(graduatedPrice(), d("500"))
	// 500 * 0.05 = 25
	assertAmount(t, got, "25.00")
}

func TestGraduatedSpansTiers(t *testing.T) {
	got, _ := Calculate(graduatedPrice(), d("6000"))
	// 1000*0.05 + 4000*0.03 + 1000*0.01 = 50 + 120 + 10 = 180
	assertAmount(t, got, "180.00")
}

func TestGraduatedExactBoundary(t *testing.T) {
	got, _ := Calculate(graduatedPrice(), d("1000"))
	// exactly fills tier 1: 1000*0.05 = 50
	assertAmount(t, got, "50.00")
}

func TestGraduatedFlatFeePerTier(t *testing.T) {
	p := domain.Price{
		Model:    domain.ModelTieredGraduated,
		Currency: "USD",
		Tiers: []domain.Tier{
			{UpTo: dp("100"), UnitAmount: d("1.00"), FlatAmount: d("10.00")},
			{UpTo: nil, UnitAmount: d("0.50"), FlatAmount: d("5.00")},
		},
	}
	got, _ := Calculate(p, d("150"))
	// tier1: 100*1.00 + 10 = 110 ; tier2: 50*0.50 + 5 = 30 ; total 140
	assertAmount(t, got, "140.00")
}

func volumePrice() domain.Price {
	return domain.Price{
		Model:    domain.ModelTieredVolume,
		Currency: "USD",
		Tiers: []domain.Tier{
			{UpTo: dp("1000"), UnitAmount: d("0.05")},
			{UpTo: dp("5000"), UnitAmount: d("0.03")},
			{UpTo: nil, UnitAmount: d("0.01")},
		},
	}
}

func TestVolumeLandsInMiddleTier(t *testing.T) {
	got, _ := Calculate(volumePrice(), d("3000"))
	// whole 3000 priced at 0.03 = 90
	assertAmount(t, got, "90.00")
}

func TestVolumeLandsInTopTier(t *testing.T) {
	got, _ := Calculate(volumePrice(), d("10000"))
	// whole 10000 at 0.01 = 100
	assertAmount(t, got, "100.00")
}

func TestVolumeBoundaryInclusive(t *testing.T) {
	got, _ := Calculate(volumePrice(), d("1000"))
	// 1000 <= 1000 -> first tier 0.05 -> 50
	assertAmount(t, got, "50.00")
}

func TestNegativeQuantityErrors(t *testing.T) {
	if _, err := Calculate(graduatedPrice(), d("-1")); err == nil {
		t.Fatal("expected error on negative quantity")
	}
}

func TestMalformedTiersError(t *testing.T) {
	cases := map[string][]domain.Tier{
		"empty":            {},
		"final bounded":    {{UpTo: dp("100"), UnitAmount: d("1")}},
		"non-ascending":    {{UpTo: dp("100"), UnitAmount: d("1")}, {UpTo: dp("50"), UnitAmount: d("1")}, {UpTo: nil, UnitAmount: d("1")}},
		"middle unbounded": {{UpTo: nil, UnitAmount: d("1")}, {UpTo: dp("100"), UnitAmount: d("1")}},
	}
	for name, tiers := range cases {
		p := domain.Price{Model: domain.ModelTieredGraduated, Currency: "USD", Tiers: tiers}
		if _, err := Calculate(p, d("10")); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
