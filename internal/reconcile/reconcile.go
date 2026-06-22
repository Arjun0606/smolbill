// Package reconcile is the headline (build plan §3 #1, §9 GET /reconcile): it
// proves a finalized invoice still agrees with the live event log, line by line.
//
// At finalize time we persist a ledger row per line (raw_event_count,
// meter_value, invoice_line_amount, verification hash). Reconcile re-derives the
// same chain from the current events and diffs it against what was stored. If
// late or out-of-order events arrived after finalize, the meter value moves and
// the diff makes it visible — the engine never silently disagrees with itself.
//
// This is pure code over plain inputs (stored ledger + a freshly computed
// invoice result), so it is fully unit-testable and deterministic.
package reconcile

import (
	"fmt"
	"sort"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/invoice"
)

// LineProof compares one invoice line as stored vs. as recomputed now.
type LineProof struct {
	MeterCode           string   `json:"meter_code"`
	StoredRawEventCount int      `json:"stored_raw_event_count"`
	LiveRawEventCount   int      `json:"live_raw_event_count"`
	StoredMeterValue    string   `json:"stored_meter_value"`
	LiveMeterValue      string   `json:"live_meter_value"`
	StoredAmount        string   `json:"stored_amount"`
	LiveAmount          string   `json:"live_amount"`
	Consistent          bool     `json:"consistent"`
	Diffs               []string `json:"diffs,omitempty"`
}

// Proof is the full reconciliation result for an invoice: the queryable audit
// trail of raw events -> meter -> invoice line, with any drift made explicit.
type Proof struct {
	InvoiceID   string      `json:"invoice_id"`
	StoredHash  string      `json:"stored_hash"`
	LiveHash    string      `json:"live_hash"`
	HashMatch   bool        `json:"hash_match"`
	Consistent  bool        `json:"consistent"` // true => meter and invoice provably agree
	StoredTotal string      `json:"stored_total"`
	LiveTotal   string      `json:"live_total"`
	Lines       []LineProof `json:"lines"`
	Diffs       []string    `json:"diffs,omitempty"` // invoice-level diffs (e.g. total drift)
	Verdict     string      `json:"verdict"`         // "consistent" | "drift_detected"
}

// Build compares a stored invoice + its ledger rows against a freshly recomputed
// invoice result, producing the proof. `storedInvoice` and `ledger` come from
// the database; `live` is invoice.Calculate run on the current event log.
func Build(storedInvoice domain.Invoice, ledger []domain.LedgerRow, live invoice.Result) Proof {
	p := Proof{
		InvoiceID:   storedInvoice.ID,
		StoredHash:  ledgerHash(ledger, storedInvoice),
		LiveHash:    live.Hash,
		StoredTotal: storedInvoice.Total.StringFixed(2),
		LiveTotal:   live.Invoice.Total.StringFixed(2),
	}
	p.HashMatch = p.StoredHash != "" && p.StoredHash == p.LiveHash

	// Index stored ledger and live traces by meter code for a stable join.
	storedByMeter := map[string]domain.LedgerRow{}
	for _, row := range ledger {
		storedByMeter[row.MeterCode] = row
	}
	liveByMeter := map[string]invoice.LineTrace{}
	for _, tr := range live.Traces {
		liveByMeter[tr.MeterCode] = tr
	}

	meters := unionKeys(storedByMeter, liveByMeter)
	for _, code := range meters {
		stored, hasStored := storedByMeter[code]
		liveT, hasLive := liveByMeter[code]

		lp := LineProof{MeterCode: code}
		var diffs []string

		switch {
		case hasStored && !hasLive:
			diffs = append(diffs, "line present in ledger but absent from live recompute")
			lp.StoredRawEventCount = stored.RawEventCount
			lp.StoredMeterValue = stored.MeterValue.String()
			lp.StoredAmount = stored.InvoiceLineAmount.StringFixed(2)
			lp.LiveMeterValue, lp.LiveAmount = "—", "—"
		case !hasStored && hasLive:
			diffs = append(diffs, "line present in live recompute but absent from ledger (new usage since finalize)")
			lp.LiveRawEventCount = liveT.RawEventCount
			lp.LiveMeterValue = liveT.MeterValue.String()
			lp.LiveAmount = liveT.Amount.StringFixed(2)
			lp.StoredMeterValue, lp.StoredAmount = "—", "—"
		default:
			lp.StoredRawEventCount = stored.RawEventCount
			lp.LiveRawEventCount = liveT.RawEventCount
			lp.StoredMeterValue = stored.MeterValue.String()
			lp.LiveMeterValue = liveT.MeterValue.String()
			lp.StoredAmount = stored.InvoiceLineAmount.StringFixed(2)
			lp.LiveAmount = liveT.Amount.StringFixed(2)

			if stored.RawEventCount != liveT.RawEventCount {
				diffs = append(diffs, fmt.Sprintf("raw event count %d -> %d (%+d, likely late/out-of-order events)",
					stored.RawEventCount, liveT.RawEventCount, liveT.RawEventCount-stored.RawEventCount))
			}
			if !stored.MeterValue.Equal(liveT.MeterValue) {
				diffs = append(diffs, fmt.Sprintf("meter value %s -> %s", stored.MeterValue, liveT.MeterValue))
			}
			if !stored.InvoiceLineAmount.Equal(liveT.Amount) {
				diffs = append(diffs, fmt.Sprintf("amount %s -> %s", stored.InvoiceLineAmount.StringFixed(2), liveT.Amount.StringFixed(2)))
			}
		}

		lp.Diffs = diffs
		lp.Consistent = len(diffs) == 0
		p.Lines = append(p.Lines, lp)
	}

	if !storedInvoice.Total.Equal(live.Invoice.Total) {
		p.Diffs = append(p.Diffs, fmt.Sprintf("invoice total %s -> %s (%s drift)",
			storedInvoice.Total.StringFixed(2), live.Invoice.Total.StringFixed(2),
			live.Invoice.Total.Sub(storedInvoice.Total).StringFixed(2)))
	}

	p.Consistent = len(p.Diffs) == 0 && allLinesConsistent(p.Lines) && p.HashMatch
	if p.Consistent {
		p.Verdict = "consistent"
	} else {
		p.Verdict = "drift_detected"
	}
	return p
}

// ledgerHash returns the verification hash recorded at finalize time. Every
// ledger row carries the same invoice-level hash; we read it from the first row,
// falling back to empty if the ledger is absent.
func ledgerHash(ledger []domain.LedgerRow, _ domain.Invoice) string {
	if len(ledger) == 0 {
		return ""
	}
	return ledger[0].ComputedHash
}

func allLinesConsistent(lines []LineProof) bool {
	for _, l := range lines {
		if !l.Consistent {
			return false
		}
	}
	return true
}

func unionKeys(a map[string]domain.LedgerRow, b map[string]invoice.LineTrace) []string {
	set := map[string]struct{}{}
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LedgerFromResult builds the ledger rows to persist for a freshly computed
// invoice (called at finalize). Each line's stored proof captures the exact
// inputs that produced its amount. Row IDs are left empty for the store to
// assign on save (mirroring how events are stored).
func LedgerFromResult(invoiceID string, res invoice.Result) []domain.LedgerRow {
	rows := make([]domain.LedgerRow, 0, len(res.Traces))
	for _, tr := range res.Traces {
		rows = append(rows, domain.LedgerRow{
			InvoiceID:         invoiceID,
			MeterCode:         tr.MeterCode,
			RawEventCount:     tr.RawEventCount,
			MeterValue:        tr.MeterValue,
			InvoiceLineAmount: tr.Amount,
			ComputedHash:      res.Hash,
		})
	}
	return rows
}
