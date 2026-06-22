package api

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/dunning"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/meter"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/reconcile"
)

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
	ents, err := s.store.EntitlementsForCustomer(customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	meters, err := s.store.Meters()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := s.store.EventsForCustomer(customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type entOut struct {
		Feature     string `json:"feature"`
		Kind        string `json:"kind"`
		MeterCode   string `json:"meter_code,omitempty"`
		Limit       string `json:"limit,omitempty"`
		Used        string `json:"used,omitempty"`
		Remaining   string `json:"remaining,omitempty"`
		WithinLimit bool   `json:"within_limit"`
		PctUsed     string `json:"pct_used,omitempty"`
	}
	out := make([]entOut, 0, len(ents))
	for _, e := range ents {
		o := entOut{Feature: e.Feature, Kind: string(e.Kind), WithinLimit: true}
		if e.Kind == domain.EntMetered {
			m, ok := meters[e.MeterCode]
			used := decimal.Zero
			if ok {
				used, _ = meter.Aggregate(m, events, e.PeriodStart, e.PeriodEnd)
			}
			remaining := e.LimitValue.Sub(used)
			o.MeterCode = e.MeterCode
			o.Limit = e.LimitValue.String()
			o.Used = used.String()
			o.Remaining = remaining.String()
			o.WithinLimit = used.LessThanOrEqual(e.LimitValue)
			if e.LimitValue.IsPositive() {
				o.PctUsed = used.Div(e.LimitValue).Mul(decimal.NewFromInt(100)).StringFixed(1)
			}
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer_id": customerID, "entitlements": out})
}
