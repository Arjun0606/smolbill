package api

import (
	"net/http"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/dunning"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/reconcile"
	"github.com/Arjun0606/smolbill/internal/revrec"
	"github.com/shopspring/decimal"
)

// revenueRecognition returns the ASC 606 straight-line recognition schedule for a
// finalized invoice as of a date (?as_of=RFC3339, default now): how much of the
// invoice is recognized revenue vs still deferred. Computed fresh from the stored
// invoice, so it never drifts.
func (s *Server) revenueRecognition(w http.ResponseWriter, r *http.Request) {
	inv, ok, err := s.store.GetInvoice(r.PathValue("invoice_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown invoice_id")
		return
	}
	asOf := s.now()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, "as_of must be RFC3339")
			return
		}
		asOf = t
	}
	writeJSON(w, http.StatusOK, revrec.Recognize(inv, asOf))
}

// --- POST /v1/invoices/finalize ---
//
// Materializes the deterministic invoice for a subscription and persists it
// together with its reconciliation ledger (build plan §9). Stripe push is
// Phase 3; here the invoice is finalized locally so the ledger exists to be
// reconciled against.

type finalizeReq struct {
	SubscriptionID string `json:"subscription_id"`
}

func (s *Server) finalizeInvoice(w http.ResponseWriter, r *http.Request) {
	var req finalizeReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, ok, err := s.store.GetSubscription(req.SubscriptionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown subscription_id")
		return
	}
	res, err := s.compute(sub)
	if err != nil {
		s.writeComputeErr(w, err)
		return
	}

	inv := res.Invoice
	inv.ID = id.New("inv")
	inv.Status = "finalized"

	// If a payment rail is configured, push to it BEFORE persisting so we never
	// record a finalized invoice the processor doesn't have. The push is
	// idempotent on the invoice id, so a retry after a transient failure is safe.
	if s.proc != nil {
		cust, _, _ := s.store.GetCustomer(inv.CustomerID)
		push, err := s.proc.PushInvoice(r.Context(), payments.PushRequest{
			Invoice: inv, Customer: cust, IdempotencyKey: inv.ID, Hash: res.Hash,
		})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "payment processor push failed: "+err.Error())
			return
		}
		inv.StripeInvoiceID = push.ExternalID
		inv.Status = push.Status
	}

	ledger := reconcile.LedgerFromResult(inv.ID, res)
	if err := s.store.SaveFinalizedInvoice(inv, ledger); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Notify subscribers the invoice is live. Fire-and-forget so a slow webhook
	// endpoint never delays the finalize response.
	go s.emit("invoice.finalized", map[string]any{
		"invoice_id":  inv.ID,
		"customer_id": inv.CustomerID,
		"total":       inv.Total.String(),
		"currency":    inv.Currency,
		"status":      inv.Status,
	})

	// If the invoice was pushed to a rail and isn't already paid, open a dunning
	// collection so recovery can begin (via /v1/dunning/run or a manual /collect).
	// The record exists the moment the bill goes out — no premium tier required.
	if s.proc != nil && inv.StripeInvoiceID != "" && inv.Status != "paid" {
		_ = s.store.PutCollection(domain.Collection{
			InvoiceID:   inv.ID,
			ExternalID:  inv.StripeInvoiceID,
			Status:      string(dunning.Scheduled),
			Currency:    inv.Currency,
			AmountMinor: money.New(inv.Total, inv.Currency).MinorUnits(),
			UpdatedAt:   s.now(),
		})
	}

	resp := invoiceResponse(res)
	resp["invoice_id"] = inv.ID
	resp["status"] = inv.Status
	if inv.StripeInvoiceID != "" {
		resp["processor"] = s.proc.Name()
		resp["external_invoice_id"] = inv.StripeInvoiceID
	}
	writeJSON(w, http.StatusCreated, resp)
}

// --- GET /v1/reconcile/{invoice_id} — THE HEADLINE ---
//
// Re-derives the invoice from the live event log and diffs it against the stored
// ledger. Returns the full raw-events -> meter -> invoice-line chain with any
// drift made explicit. If late/out-of-order events arrived after finalize, the
// verdict flips to "drift_detected" and the diffs say exactly what moved — the
// engine can never silently disagree with itself.

func (s *Server) reconcileInvoice(w http.ResponseWriter, r *http.Request) {
	invID := r.PathValue("invoice_id")
	storedInv, ok, err := s.store.GetInvoice(invID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown invoice_id")
		return
	}
	ledger, err := s.store.GetLedger(invID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	live, err := s.recomputeForInvoice(storedInv)
	if err != nil {
		s.writeComputeErr(w, err)
		return
	}
	proof := reconcile.Build(storedInv, ledger, live)

	status := http.StatusOK
	if !proof.Consistent {
		// Surface drift loudly. The data is still served (200-family), but a
		// distinct code lets monitoring alert on it — and a webhook fires so the
		// operator hears about it without polling the endpoint.
		status = http.StatusConflict
		go s.emit("drift.detected", map[string]any{
			"invoice_id":   invID,
			"scope":        "ledger",
			"stored_total": storedInv.Total.String(),
		})
	}
	writeJSON(w, status, proof)
}

// verifyInvoice reconciles ACROSS the money rail: it asks the payment processor
// (any processor — Stripe today, Paddle/Dodo tomorrow) what it ACTUALLY billed
// for this invoice and asserts it equals the ledger total to the exact minor
// unit. This catches drift the processor introduced — a tax line, a manual edit,
// a rounding rule — that an internal-only reconciliation can never see. 200 if
// they agree, 409 with the delta if the processor billed something different.
func (s *Server) verifyInvoice(w http.ResponseWriter, r *http.Request) {
	inv, ok, err := s.store.GetInvoice(r.PathValue("invoice_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown invoice_id")
		return
	}
	if s.proc == nil || inv.StripeInvoiceID == "" {
		writeErr(w, http.StatusBadRequest, "invoice has not been pushed to a payment processor")
		return
	}
	fetched, err := s.proc.FetchInvoice(r.Context(), inv.StripeInvoiceID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	ledgerMinor := money.New(inv.Total, inv.Currency).MinorUnits()
	consistent := fetched.AmountMinor == ledgerMinor
	resp := map[string]any{
		"invoice_id":             inv.ID,
		"processor":              s.proc.Name(),
		"external_invoice_id":    inv.StripeInvoiceID,
		"ledger_amount_minor":    ledgerMinor,
		"processor_amount_minor": fetched.AmountMinor,
		"currency":               inv.Currency,
		"consistent":             consistent,
	}
	status := http.StatusOK
	if !consistent {
		resp["drift_minor"] = fetched.AmountMinor - ledgerMinor
		status = http.StatusConflict
		go s.emit("drift.detected", map[string]any{
			"invoice_id":  inv.ID,
			"scope":       "processor",
			"processor":   s.proc.Name(),
			"drift_minor": fetched.AmountMinor - ledgerMinor,
		})
	}
	writeJSON(w, status, resp)
}

// --- GET /v1/reconcile — ACCOUNT-WIDE DRIFT SCAN ---
//
// Reconciles EVERY finalized invoice against a fresh recompute from the live event
// log and reports how many drifted plus the total money at risk. This is the
// "verify in the engine, not in a BigQuery shadow ledger" capability: revenue
// leakage (industry estimates 1-5% of revenue) made provable and quantified in one
// call. Complements the per-invoice GET /v1/reconcile/{invoice_id}.
func (s *Server) scanDrift(w http.ResponseWriter, r *http.Request) {
	invs, err := s.store.ListInvoices()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type drifted struct {
		InvoiceID   string   `json:"invoice_id"`
		CustomerID  string   `json:"customer_id"`
		StoredTotal string   `json:"stored_total"`
		LiveTotal   string   `json:"live_total"`
		Drift       string   `json:"drift"`
		Currency    string   `json:"currency"`
		Diffs       []string `json:"diffs,omitempty"`
	}
	var scanned, consistent int
	atRisk := map[string]decimal.Decimal{}
	drifts := make([]drifted, 0)

	for _, inv := range invs {
		// Only finalized invoices have a reconciliation ledger to check against.
		ledger, lerr := s.store.GetLedger(inv.ID)
		if lerr != nil || len(ledger) == 0 {
			continue
		}
		live, rerr := s.recomputeForInvoice(inv)
		if rerr != nil {
			continue
		}
		proof := reconcile.Build(inv, ledger, live)
		scanned++
		if proof.Consistent {
			consistent++
			continue
		}
		d := inv.Total.Sub(live.Invoice.Total).Abs()
		atRisk[inv.Currency] = atRisk[inv.Currency].Add(d)
		drifts = append(drifts, drifted{
			InvoiceID: inv.ID, CustomerID: inv.CustomerID,
			StoredTotal: money.Format(inv.Total, inv.Currency),
			LiveTotal:   money.Format(live.Invoice.Total, inv.Currency),
			Drift:       money.Format(d, inv.Currency),
			Currency:    inv.Currency, Diffs: proof.Diffs,
		})
	}

	atRiskOut := map[string]string{}
	for cur, amt := range atRisk {
		atRiskOut[cur] = money.Format(amt, cur)
	}
	verdict := "all_consistent"
	if len(drifts) > 0 {
		verdict = "drift_detected"
	}
	status := http.StatusOK
	if len(drifts) > 0 {
		status = http.StatusConflict // lets monitoring alert on account-wide drift
	}
	writeJSON(w, status, map[string]any{
		"scanned":        scanned,
		"consistent":     consistent,
		"drifted":        len(drifts),
		"amount_at_risk": atRiskOut,
		"invoices":       drifts,
		"verdict":        verdict,
	})
}

// recomputeForInvoice runs the deterministic engine over the CURRENT event log
// for the exact period the stored invoice covered, so reconcile compares like
// with like even if the subscription's period has since rolled forward.
func (s *Server) recomputeForInvoice(inv domain.Invoice) (invoice.Result, error) {
	sub, ok, err := s.store.GetSubscription(inv.SubscriptionID)
	if err != nil {
		return invoice.Result{}, err
	}
	if !ok {
		return invoice.Result{}, engine.ErrNoActiveSub
	}
	// Pin the window to the invoice's period.
	sub.CurrentPeriodStart = inv.PeriodStart
	sub.CurrentPeriodEnd = inv.PeriodEnd
	return s.compute(sub)
}

// --- entitlements ---

type entitlementReq struct {
	CustomerID  string `json:"customer_id"`
	Feature     string `json:"feature"`
	Kind        string `json:"kind"`        // boolean | metered
	MeterCode   string `json:"meter_code"`  // for metered
	LimitValue  string `json:"limit_value"` // for metered
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}

func (s *Server) createEntitlement(w http.ResponseWriter, r *http.Request) {
	var req entitlementReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	kind := domain.EntitlementKind(req.Kind)
	if kind != domain.EntBoolean && kind != domain.EntMetered {
		writeErr(w, http.StatusBadRequest, "kind must be boolean|metered")
		return
	}
	if req.CustomerID == "" || req.Feature == "" {
		writeErr(w, http.StatusBadRequest, "customer_id and feature are required")
		return
	}
	if _, ok, _ := s.store.GetCustomer(req.CustomerID); !ok {
		writeErr(w, http.StatusBadRequest, "unknown customer_id")
		return
	}
	e := domain.Entitlement{
		ID: id.New("ent"), CustomerID: req.CustomerID, Feature: req.Feature, Kind: kind,
		MeterCode: req.MeterCode, LimitValue: parseDecOr0(req.LimitValue),
	}
	if kind == domain.EntMetered && req.MeterCode == "" {
		writeErr(w, http.StatusBadRequest, "metered entitlement requires meter_code")
		return
	}
	if req.PeriodStart != "" {
		t, err := time.Parse(time.RFC3339, req.PeriodStart)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "period_start must be RFC3339")
			return
		}
		e.PeriodStart = t
	}
	if req.PeriodEnd != "" {
		t, err := time.Parse(time.RFC3339, req.PeriodEnd)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "period_end must be RFC3339")
			return
		}
		e.PeriodEnd = t
	}
	if err := s.store.PutEntitlement(e); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

// getEntitlements returns the customer's entitlements with LIVE usage for
// metered ones (derived from the event log, never a trusted counter), plus the
// remaining allowance and whether they are within limit. This is the real-time
// limit check from §9.
func (s *Server) getEntitlements(w http.ResponseWriter, r *http.Request) {
	customerID := r.PathValue("customer_id")
	// Shared with the MCP server's check_entitlement, so an agent and a human can
	// never get different answers to "is this customer within their limit?".
	out, err := engine.CheckEntitlements(s.store, customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer_id": customerID, "entitlements": out})
}
