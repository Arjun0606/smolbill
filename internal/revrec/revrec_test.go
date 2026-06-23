package revrec

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func sampleInvoice() domain.Invoice {
	return domain.Invoice{
		ID:          "inv_1",
		Currency:    "USD",
		PeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), // 30-day period
		Total:       d("100.00"),
		Lines: []domain.InvoiceLine{
			{ID: "l1", MeterCode: "", Amount: d("60.00")},       // flat
			{ID: "l2", MeterCode: "tokens", Amount: d("40.00")}, // usage
		},
	}
}

func TestFullyDeferredBeforePeriod(t *testing.T) {
	r := Recognize(sampleInvoice(), time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if r.TotalRecognized != "0.00" || r.TotalDeferred != "100.00" {
		t.Fatalf("before period: recognized=%s deferred=%s, want 0.00 / 100.00", r.TotalRecognized, r.TotalDeferred)
	}
}

func TestFullyRecognizedAfterPeriod(t *testing.T) {
	r := Recognize(sampleInvoice(), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if r.TotalRecognized != "100.00" || r.TotalDeferred != "0.00" {
		t.Fatalf("after period: recognized=%s deferred=%s, want 100.00 / 0.00", r.TotalRecognized, r.TotalDeferred)
	}
}

func TestHalfwayRecognition(t *testing.T) {
	// 15 of 30 days elapsed -> 50% recognized straight-line.
	r := Recognize(sampleInvoice(), time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC))
	if r.TotalRecognized != "50.00" || r.TotalDeferred != "50.00" {
		t.Fatalf("halfway: recognized=%s deferred=%s, want 50.00 / 50.00", r.TotalRecognized, r.TotalDeferred)
	}
	// Per line, halfway: 30.00 and 20.00 recognized.
	got := map[string]string{}
	for _, ln := range r.Lines {
		got[ln.LineID] = ln.Recognized
	}
	if got["l1"] != "30.00" || got["l2"] != "20.00" {
		t.Fatalf("per-line recognized = %v, want l1=30.00 l2=20.00", got)
	}
}

func TestDeterministic(t *testing.T) {
	asOf := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	a := Recognize(sampleInvoice(), asOf)
	b := Recognize(sampleInvoice(), asOf)
	if a.TotalRecognized != b.TotalRecognized || a.Fraction != b.Fraction {
		t.Fatalf("non-deterministic: %s/%s vs %s/%s", a.TotalRecognized, a.Fraction, b.TotalRecognized, b.Fraction)
	}
}

func TestRecognizedPlusDeferredEqualsTotal(t *testing.T) {
	// The invariant: at any point, recognized + deferred == total, exactly.
	for _, day := range []int{1, 7, 16, 23, 30} {
		r := Recognize(sampleInvoice(), time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC))
		rec := d(r.TotalRecognized)
		def := d(r.TotalDeferred)
		if !rec.Add(def).Equal(d("100.00")) {
			t.Fatalf("day %d: %s + %s != 100.00", day, r.TotalRecognized, r.TotalDeferred)
		}
	}
}
