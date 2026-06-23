// Package revrec computes revenue recognition (ASC 606) for a finalized invoice.
//
// The basic, open-core model is straight-line recognition over the service period:
// revenue is earned evenly across [PeriodStart, PeriodEnd]. Before the period starts
// the whole invoice is deferred; after it ends, fully recognized; in between, the
// time-elapsed fraction is recognized and the rest stays deferred. This is the #1
// feature teams leave other open-source billing tools for, and smolbill ships it free.
//
// Like the reconciliation ledger, it is a PURE function of the stored invoice + an
// as-of date: same inputs always yield the same schedule, so it never drifts and
// needs no extra table. Money math is exact (no floats) and rounds toward under-
// recognition at the line boundary.
//
// (Advanced recognition — per-event point-in-time usage timing, multi-standard,
// ERP export — is the Pro layer; this is the honest, correct baseline.)
package revrec

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/money"
)

// LineRecognition is the recognized/deferred split for one invoice line.
type LineRecognition struct {
	LineID     string `json:"line_id"`
	MeterCode  string `json:"meter_code"`
	Amount     string `json:"amount"`
	Recognized string `json:"recognized"`
	Deferred   string `json:"deferred"`
}

// Recognition is the full schedule for an invoice as of a date.
type Recognition struct {
	InvoiceID       string            `json:"invoice_id"`
	Currency        string            `json:"currency"`
	Method          string            `json:"method"`
	AsOf            time.Time         `json:"as_of"`
	PeriodStart     time.Time         `json:"period_start"`
	PeriodEnd       time.Time         `json:"period_end"`
	Fraction        string            `json:"recognized_fraction"`
	Lines           []LineRecognition `json:"lines"`
	TotalAmount     string            `json:"total_amount"`
	TotalRecognized string            `json:"total_recognized"`
	TotalDeferred   string            `json:"total_deferred"`
}

// Recognize computes straight-line ASC 606 recognition for inv as of asOf.
func Recognize(inv domain.Invoice, asOf time.Time) Recognition {
	frac := recognizedFraction(inv.PeriodStart, inv.PeriodEnd, asOf)
	r := Recognition{
		InvoiceID:   inv.ID,
		Currency:    inv.Currency,
		Method:      "straight_line",
		AsOf:        asOf.UTC(),
		PeriodStart: inv.PeriodStart.UTC(),
		PeriodEnd:   inv.PeriodEnd.UTC(),
		Fraction:    frac.StringFixed(6),
	}
	totalRec := money.Zero(inv.Currency)
	for _, ln := range inv.Lines {
		amt := money.New(ln.Amount, inv.Currency).RoundDown()
		rec := money.New(ln.Amount, inv.Currency).MulFraction(frac).RoundDown()
		def := amt.Sub(rec)
		r.Lines = append(r.Lines, LineRecognition{
			LineID:     ln.ID,
			MeterCode:  ln.MeterCode,
			Amount:     amt.Amount(),
			Recognized: rec.Amount(),
			Deferred:   def.Amount(),
		})
		totalRec = totalRec.Add(rec)
	}
	total := money.New(inv.Total, inv.Currency).RoundDown()
	r.TotalAmount = total.Amount()
	r.TotalRecognized = totalRec.Amount()
	r.TotalDeferred = total.Sub(totalRec).Amount()
	return r
}

// recognizedFraction is the share of the service period elapsed by asOf, in [0,1],
// computed in exact nanoseconds (time-exact, like proration).
func recognizedFraction(start, end, asOf time.Time) decimal.Decimal {
	if !end.After(start) {
		return decimal.NewFromInt(1) // zero-length/invalid period: nothing to defer
	}
	if !asOf.After(start) {
		return decimal.Zero
	}
	if !asOf.Before(end) {
		return decimal.NewFromInt(1)
	}
	elapsed := decimal.NewFromInt(asOf.Sub(start).Nanoseconds())
	total := decimal.NewFromInt(end.Sub(start).Nanoseconds())
	return elapsed.DivRound(total, 12)
}
