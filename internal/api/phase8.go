package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Arjun0606/smolbill/internal/comms"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/dunning"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/payments"
)

// processorWebhook handles an inbound webhook from the configured payment rail.
// Mounted OUTSIDE /v1 because it carries no API key — the signature is the auth —
// and it isn't part of the SDK surface. On a verified success/failure it advances
// the matching collection through the SAME dunning state machine as an off-session
// retry, so a webhook-driven recovery and a pull-driven one can never disagree.
func (s *Server) processorWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("processor")
	if s.proc == nil || !strings.EqualFold(s.proc.Name(), name) {
		writeErr(w, http.StatusBadRequest, "no matching payment processor is configured")
		return
	}
	verifier, ok := s.proc.(payments.InboundWebhooker)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "this processor does not support inbound webhooks")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read error")
		return
	}
	ev, err := verifier.VerifyWebhook(r.Header, body)
	if err != nil {
		// A bad signature is a 400 — never act on an unverified event.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.applyProcessorEvent(ev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// applyProcessorEvent advances the collection for ev.InvoiceID. Unknown/ignored
// events, missing collections, and already-terminal collections are no-ops — we
// still return 200 so the processor doesn't retry the delivery forever.
func (s *Server) applyProcessorEvent(ev payments.ProcessorEvent) {
	if ev.Kind == "ignored" || ev.InvoiceID == "" {
		return
	}
	col, ok, err := s.store.GetCollection(ev.InvoiceID)
	if err != nil || !ok || dunning.Status(col.Status).Terminal() {
		return
	}
	now := s.now()
	prev := dunning.Status(col.Status)
	if ev.Kind == "action_required" {
		col.Status = string(dunning.RequiresAction)
		col.NextAttemptAt = nil
		col.UpdatedAt = now
	} else {
		next := toState(col).RecordAttempt(ev.Kind == "paid", ev.Reason, dunning.DefaultSchedule, now)
		col = applyState(col, next, now)
	}
	if err := s.store.PutCollection(col); err != nil {
		return
	}
	s.emitCollectionEvent(prev, col)
}

// errNoProcessor is returned by dunning when no payment rail is configured.
var errNoProcessor = errors.New("no payment processor configured; dunning needs a rail")

// --- dunning / failed-payment recovery (Phase 8) ---
//
// Three surfaces over the dunning state machine:
//   POST /v1/invoices/{id}/collect    — attempt collection now (manual trigger)
//   GET  /v1/invoices/{id}/collection — inspect recovery state (no black box)
//   POST /v1/dunning/run              — process every due collection (the cron tick)
//
// A finalize on an unpaid invoice auto-creates a `scheduled` collection, so the
// recovery record exists the moment a bill goes out. Every attempt routes by
// decline reason and fires a webhook on each state change.

// collectInvoice attempts a charge for one invoice now, regardless of whether its
// next retry is due — the operator's manual "try again" (e.g. after the customer
// updated their card from a requires_action state).
func (s *Server) collectInvoice(w http.ResponseWriter, r *http.Request) {
	if s.proc == nil {
		writeErr(w, http.StatusBadRequest, "no payment processor configured; collection needs a rail")
		return
	}
	col, ok, err := s.store.GetCollection(r.PathValue("invoice_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no collection for invoice_id (finalize it with a processor first)")
		return
	}
	if dunning.Status(col.Status).Terminal() {
		// Already paid or written off — nothing to do; return the state idempotently.
		writeJSON(w, http.StatusOK, col)
		return
	}
	updated, err := s.attemptCollection(r.Context(), col)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "charge attempt failed at the processor: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// getCollection returns the recovery state for an invoice — every attempt, the
// reason for the last failure, and when the next retry fires. The transparency
// the incumbents charge for.
func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) {
	col, ok, err := s.store.GetCollection(r.PathValue("invoice_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no collection for invoice_id")
		return
	}
	writeJSON(w, http.StatusOK, col)
}

// runDunning processes every collection whose next attempt is due (or that has
// never been attempted). This is the endpoint a cron/scheduler hits on a cadence;
// it is idempotent — only due collections are touched, each at most once per call.
func (s *Server) runDunning(w http.ResponseWriter, r *http.Request) {
	if s.proc == nil {
		writeErr(w, http.StatusBadRequest, "no payment processor configured; dunning needs a rail")
		return
	}
	processed, faults, summary, err := s.RunDueDunning(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"processed":  processed,
		"faults":     faults,
		"by_status":  summary,
		"checked_at": s.now(),
	})
}

// RunDueDunning processes every collection whose retry is due, once each. It's the
// shared core behind POST /v1/dunning/run and the optional scheduler in serve.
// Idempotent and safe to call on a cadence; a processor must be configured.
func (s *Server) RunDueDunning(ctx context.Context) (processed, faults int, byStatus map[string]int, err error) {
	if s.proc == nil {
		return 0, 0, nil, errNoProcessor
	}
	due, err := s.store.CollectionsDue(s.now())
	if err != nil {
		return 0, 0, nil, err
	}
	byStatus = map[string]int{}
	for _, col := range due {
		updated, aerr := s.attemptCollection(ctx, col)
		if aerr != nil {
			faults++ // transport fault; state left unchanged, picked up next run
			continue
		}
		byStatus[updated.Status]++
	}
	return len(due), faults, byStatus, nil
}

// attemptCollection runs one charge attempt, folds the outcome through the pure
// dunning state machine, persists the new state, and fires the matching webhook.
// A transport fault (err != nil) leaves state untouched so the next run retries.
func (s *Server) attemptCollection(ctx context.Context, col domain.Collection) (domain.Collection, error) {
	res, err := s.proc.ChargeInvoice(ctx, col.ExternalID)
	if err != nil {
		return col, err
	}
	now := s.now()
	prev := dunning.Status(col.Status)
	next := toState(col).RecordAttempt(res.Status == "paid", res.FailureReason, dunning.DefaultSchedule, now)
	col = applyState(col, next, now)
	if err := s.store.PutCollection(col); err != nil {
		return col, err
	}
	s.emitCollectionEvent(prev, col)
	return col, nil
}

// emitCollectionEvent fires exactly one webhook per logical recovery transition —
// not one per raw retry — carrying the attempt count, reason, and next retry time
// so a consumer never has to poll. (Stripe's invoice.payment_failed firing on
// every retry, with the reason buried, is a documented source of dunning spam.)
func (s *Server) emitCollectionEvent(prev dunning.Status, col domain.Collection) {
	data := map[string]any{
		"invoice_id":   col.InvoiceID,
		"status":       col.Status,
		"attempts":     col.Attempts,
		"last_reason":  col.LastReason,
		"amount_minor": col.AmountMinor,
		"currency":     col.Currency,
	}
	if col.NextAttemptAt != nil {
		data["next_attempt_at"] = *col.NextAttemptAt
	}
	switch dunning.Status(col.Status) {
	case dunning.Paid:
		// A success after any prior failure is a recovery; a clean first charge is
		// just a payment.
		if col.Attempts > 1 || prev == dunning.Retrying || prev == dunning.RequiresAction {
			data["message"] = s.renderDunningMessage(comms.Recovered, col)
			go s.emit("invoice.recovered", data)
		} else {
			go s.emit("invoice.paid", data)
		}
	case dunning.Retrying:
		data["message"] = s.renderDunningMessage(comms.PaymentFailed, col)
		go s.emit("invoice.payment_failed", data)
	case dunning.RequiresAction:
		data["message"] = s.renderDunningMessage(comms.RequiresAction, col)
		go s.emit("invoice.action_required", data)
	case dunning.Uncollectible:
		data["message"] = s.renderDunningMessage(comms.Uncollectible, col)
		go s.emit("invoice.uncollectible", data)
	}
}

// analytics returns the account-wide snapshot: customers, projected + finalized
// revenue by currency, and dunning recovery. Computed live through the same
// engine that bills, never a cached counter — the revenue visibility Stripe gates
// behind paid Sigma and Baremetrics sells as a product.
func (s *Server) analytics(w http.ResponseWriter, r *http.Request) {
	a, err := engine.ComputeAnalytics(s.store, s.now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// --- mapping between the persisted Collection and the pure dunning.State ---

func toState(c domain.Collection) dunning.State {
	st := dunning.State{
		Status:     dunning.Status(c.Status),
		Attempts:   c.Attempts,
		LastReason: c.LastReason,
	}
	if c.FirstFailedAt != nil {
		st.FirstFailedAt = *c.FirstFailedAt
	}
	if c.NextAttemptAt != nil {
		st.NextAttemptAt = *c.NextAttemptAt
	}
	return st
}

func applyState(c domain.Collection, st dunning.State, now time.Time) domain.Collection {
	c.Status = string(st.Status)
	c.Attempts = st.Attempts
	c.LastReason = st.LastReason
	c.FirstFailedAt = nilIfZero(st.FirstFailedAt)
	c.NextAttemptAt = nilIfZero(st.NextAttemptAt)
	c.UpdatedAt = now
	return c
}

func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
