// Package api is the HTTP layer: the `/v1` REST surface from build plan §9. It
// is a thin shell over the deterministic engine — it parses intent, calls the
// store and the pure math packages, and serializes the result. No money math
// lives here.
//
// Routing uses the net/http 1.22+ method+pattern mux, so there are no framework
// dependencies (the single-binary promise).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/alerts"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/store"
	"github.com/Arjun0606/smolbill/internal/webhook"
)

// Server holds the dependencies of the HTTP layer.
type Server struct {
	store    store.Store
	ing      *ingest.Ingester
	now      func() time.Time   // injectable clock for deterministic tests
	proc     payments.Processor // optional payment rail; nil => finalize is local-only
	notifier alerts.Notifier    // spend-alert delivery
	whSender webhook.Sender     // outbound lifecycle-event delivery
}

// New builds a Server. A nil clock defaults to time.Now (UTC).
func New(st store.Store, ing *ingest.Ingester, clock func() time.Time) *Server {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Server{store: st, ing: ing, now: clock, notifier: alerts.NewWebhookNotifier(), whSender: webhook.NewHTTPSender()}
}

// SetProcessor attaches a payment rail (e.g. Stripe). When set, finalize pushes
// the materialized invoice to the processor; when nil, finalize is local-only.
func (s *Server) SetProcessor(p payments.Processor) { s.proc = p }

// SetNotifier overrides the spend-alert notifier (tests inject a recorder).
func (s *Server) SetNotifier(n alerts.Notifier) { s.notifier = n }

// SetWebhookSender overrides the webhook delivery transport (tests inject a recorder).
func (s *Server) SetWebhookSender(snd webhook.Sender) { s.whSender = snd }

// emit delivers a lifecycle event to every webhook subscribed to its type (or to
// all events). It is best-effort: a failing endpoint is logged, never blocked on,
// and never affects the billing operation that produced the event. Call sites
// wrap this in a goroutine so webhook latency can't delay an API response.
func (s *Server) emit(eventType string, data map[string]any) {
	hooks, err := s.store.Webhooks()
	if err != nil {
		log.Printf("webhook: list failed: %v", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	ev := webhook.Event{ID: id.New("evt"), Type: eventType, OccurredAt: s.now(), Data: data}
	for _, h := range hooks {
		if !subscribedTo(h, eventType) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := s.whSender.Send(ctx, h.URL, h.Secret, ev); err != nil {
			log.Printf("webhook: deliver %s to %s failed: %v", eventType, h.URL, err)
		}
		cancel()
	}
}

// subscribedTo reports whether a webhook wants this event type. An empty Events
// slice means "all events" (subscribe to everything).
func subscribedTo(h domain.Webhook, eventType string) bool {
	return len(h.Events) == 0 || slices.Contains(h.Events, eventType)
}

// Handler returns the routed http.Handler for the whole /v1 surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/customers", s.createCustomer)
	mux.HandleFunc("POST /v1/meters", s.createMeter)
	mux.HandleFunc("POST /v1/plans", s.createPlan)
	mux.HandleFunc("POST /v1/subscriptions", s.createSubscription)
	mux.HandleFunc("POST /v1/events", s.ingestEvent)
	mux.HandleFunc("GET /v1/usage/{customer_id}", s.usage)
	mux.HandleFunc("POST /v1/invoices/preview", s.previewInvoice)
	mux.HandleFunc("POST /v1/invoices/simulate", s.simulateInvoice)
	mux.HandleFunc("POST /v1/invoices/finalize", s.finalizeInvoice)
	mux.HandleFunc("GET /v1/reconcile/{invoice_id}", s.reconcileInvoice)
	mux.HandleFunc("GET /v1/invoices/{invoice_id}/verify", s.verifyInvoice)
	mux.HandleFunc("POST /v1/invoices/{invoice_id}/collect", s.collectInvoice)
	mux.HandleFunc("GET /v1/invoices/{invoice_id}/collection", s.getCollection)
	mux.HandleFunc("POST /v1/dunning/run", s.runDunning)
	mux.HandleFunc("GET /v1/analytics", s.analytics)
	mux.HandleFunc("POST /v1/entitlements", s.createEntitlement)
	mux.HandleFunc("GET /v1/entitlements/{customer_id}", s.getEntitlements)
	mux.HandleFunc("POST /v1/alerts", s.createAlert)
	mux.HandleFunc("POST /v1/webhooks", s.createWebhook)
	mux.HandleFunc("GET /v1/webhooks", s.listWebhooks)

	// Wallet (free, OSS-core — the feature Lago charges ~$1,500/mo for).
	mux.HandleFunc("POST /v1/wallet/{customer_id}/topup", s.topupWallet)
	mux.HandleFunc("GET /v1/wallet/{customer_id}", s.getWallet)

	// Dashboard (server-rendered, embedded in the binary) + embeddable portal.
	mux.HandleFunc("GET /dashboard", s.dashboardHome)
	mux.HandleFunc("GET /dashboard/customers/{customer_id}", s.dashboardCustomer)
	mux.HandleFunc("GET /dashboard/invoices/{invoice_id}/reconcile", s.dashboardReconcile)
	mux.HandleFunc("GET /portal/{customer_id}", s.portal)
	return mux
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- customers ---

type customerReq struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var req customerReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	c := domain.Customer{ID: id.New("cus"), ExternalID: req.ExternalID, Name: req.Name, CreatedAt: s.now()}
	if err := s.store.PutCustomer(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// --- meters ---

type meterReq struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Aggregation string `json:"aggregation"`
	PropertyKey string `json:"property_key"`
}

func (s *Server) createMeter(w http.ResponseWriter, r *http.Request) {
	var req meterReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	agg := domain.Aggregation(req.Aggregation)
	switch agg {
	case domain.AggCount, domain.AggSum, domain.AggMax, domain.AggUnique:
	default:
		writeErr(w, http.StatusBadRequest, "aggregation must be count|sum|max|unique")
		return
	}
	if req.Code == "" {
		writeErr(w, http.StatusBadRequest, "code is required")
		return
	}
	if agg != domain.AggCount && req.PropertyKey == "" {
		writeErr(w, http.StatusBadRequest, "property_key is required for "+req.Aggregation)
		return
	}
	m := domain.Meter{ID: id.New("mtr"), Code: req.Code, Name: req.Name, Aggregation: agg, PropertyKey: req.PropertyKey, CreatedAt: s.now()}
	if err := s.store.PutMeter(m); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// --- plans ---

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var in engine.PlanInput
	if err := decodeBody(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := engine.BuildPlan(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	plan.CreatedAt = s.now()
	if err := s.store.PutPlan(plan); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

// --- subscriptions (attach a plan to a customer) ---

type subscriptionReq struct {
	CustomerID  string `json:"customer_id"`
	PlanID      string `json:"plan_id"`
	PeriodStart string `json:"period_start"` // RFC3339
	PeriodEnd   string `json:"period_end"`   // RFC3339
	StartedAt   string `json:"started_at"`   // optional RFC3339; defaults to period_start
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok, _ := s.store.GetCustomer(req.CustomerID); !ok {
		writeErr(w, http.StatusBadRequest, "unknown customer_id")
		return
	}
	plan, ok, err := s.store.GetPlan(req.PlanID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown plan_id")
		return
	}
	ps, err := time.Parse(time.RFC3339, req.PeriodStart)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "period_start must be RFC3339")
		return
	}
	pe, err := time.Parse(time.RFC3339, req.PeriodEnd)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "period_end must be RFC3339")
		return
	}
	startedAt := ps
	if req.StartedAt != "" {
		if startedAt, err = time.Parse(time.RFC3339, req.StartedAt); err != nil {
			writeErr(w, http.StatusBadRequest, "started_at must be RFC3339")
			return
		}
	}
	sub := domain.Subscription{
		ID: id.New("sub"), CustomerID: req.CustomerID, PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: ps, CurrentPeriodEnd: pe, StartedAt: startedAt,
	}
	if err := s.store.PutSubscription(sub); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

// --- events (idempotent ingest) ---

type eventReq struct {
	IdempotencyKey string         `json:"idempotency_key"`
	CustomerID     string         `json:"customer_id"`
	MeterCode      string         `json:"meter_code"`
	EventTime      string         `json:"event_time"` // RFC3339
	Properties     map[string]any `json:"properties"`
}

func (s *Server) ingestEvent(w http.ResponseWriter, r *http.Request) {
	var req eventReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	et, err := time.Parse(time.RFC3339, req.EventTime)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "event_time must be RFC3339")
		return
	}
	e := domain.Event{
		ID: id.New("evt"), IdempotencyKey: req.IdempotencyKey, CustomerID: req.CustomerID,
		MeterCode: req.MeterCode, EventTime: et, Properties: req.Properties,
	}
	accepted, err := s.ing.Accept(e, s.now())
	if errors.Is(err, ingest.ErrDuplicate) {
		// Idempotent: a duplicate is a successful no-op, surfaced honestly.
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "duplicate", "idempotency_key": req.IdempotencyKey,
			"message": "already recorded; no-op (idempotent)",
		})
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Usage changed — proactively evaluate spend alerts (best-effort).
	s.evaluateAlerts(r.Context(), accepted.CustomerID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "accepted", "event_id": accepted.ID,
		"dedup_window": s.ing.Window().String(),
	})
}

// --- usage (real-time current-period usage + projected bill) ---

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	customerID := r.PathValue("customer_id")

	// Time-travel: ?as_of=<RFC3339> replays the bill from only the events known at
	// that moment — the audit/dispute view nothing snapshot-based can give.
	compute := s.computeForActiveSub
	var asOf time.Time
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, "as_of must be RFC3339, e.g. 2026-06-15T00:00:00Z")
			return
		}
		asOf = t
		compute = func(cid string) (invoice.Result, domain.Subscription, error) {
			return engine.ComputeAsOfForActiveSub(s.store, cid, t)
		}
	}

	res, sub, err := compute(customerID)
	if err != nil {
		s.writeComputeErr(w, err)
		return
	}
	type meterUsage struct {
		MeterCode string `json:"meter_code"`
		Quantity  string `json:"quantity"`
		Amount    string `json:"amount"`
	}
	var usages []meterUsage
	for _, tr := range res.Traces {
		code := tr.MeterCode
		if code == "" {
			code = "(base fee)"
		}
		usages = append(usages, meterUsage{code, tr.MeterValue.String(), tr.Amount.StringFixed(2)})
	}
	resp := map[string]any{
		"customer_id":     customerID,
		"subscription_id": sub.ID,
		"period_start":    sub.CurrentPeriodStart,
		"period_end":      sub.CurrentPeriodEnd,
		"usage":           usages,
		"projected_total": money.Format(res.Invoice.Total, res.Invoice.Currency),
		"currency":        res.Invoice.Currency,
	}
	if !asOf.IsZero() {
		resp["as_of"] = asOf
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- invoice preview (deterministic, exact) ---

type previewReq struct {
	SubscriptionID string `json:"subscription_id"`
}

func (s *Server) previewInvoice(w http.ResponseWriter, r *http.Request) {
	var req previewReq
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
	writeJSON(w, http.StatusOK, invoiceResponse(res))
}

// simulateReq proposes a plan and asks what it WOULD bill the customer this
// period, against their real usage. Nothing is persisted.
type simulateReq struct {
	CustomerID string           `json:"customer_id"`
	Plan       engine.PlanInput `json:"plan"`
}

// simulateInvoice is the sandbox endpoint: replay the customer's real event log
// against a proposed plan and return the diff vs their live bill, committing
// nothing. The same engine that finalizes invoices computes the preview.
func (s *Server) simulateInvoice(w http.ResponseWriter, r *http.Request) {
	var req simulateReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CustomerID == "" {
		writeErr(w, http.StatusBadRequest, "customer_id required")
		return
	}
	res, err := engine.SimulatePlanChange(s.store, req.CustomerID, req.Plan)
	if err != nil {
		s.writeComputeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, simulationResponse(res))
}

// simulationResponse renders a sandbox result with every money field formatted to
// the currency's minor unit (like every other endpoint), so the API is uniformly
// "64.00", never a raw "64".
func simulationResponse(res engine.SimulationResult) map[string]any {
	cur := res.Currency
	lines := make([]map[string]any, 0, len(res.Lines))
	for _, l := range res.Lines {
		lines = append(lines, map[string]any{
			"meter_code":      l.MeterCode,
			"current_amount":  money.Format(l.CurrentAmount, cur),
			"proposed_amount": money.Format(l.ProposedAmount, cur),
			"delta":           money.Format(l.Delta, cur),
		})
	}
	return map[string]any{
		"customer_id":    res.CustomerID,
		"period_start":   res.PeriodStart,
		"period_end":     res.PeriodEnd,
		"currency":       cur,
		"current_total":  money.Format(res.CurrentTotal, cur),
		"proposed_total": money.Format(res.ProposedTotal, cur),
		"delta":          money.Format(res.Delta, cur),
		"lines":          lines,
		"current_hash":   res.CurrentHash,
		"proposed_hash":  res.ProposedHash,
	}
}

// --- shared computation ---

// compute and computeForActiveSub delegate to the shared engine so the REST API
// and the MCP server use one code path.
func (s *Server) computeForActiveSub(customerID string) (invoice.Result, domain.Subscription, error) {
	return engine.ComputeForActiveSub(s.store, customerID)
}

func (s *Server) compute(sub domain.Subscription) (invoice.Result, error) {
	return engine.Compute(s.store, sub)
}

func (s *Server) writeComputeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNoActiveSub):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func invoiceResponse(res invoice.Result) map[string]any {
	type line struct {
		MeterCode       string `json:"meter_code"`
		Model           string `json:"model"`
		RawEventCount   int    `json:"raw_event_count"`
		Quantity        string `json:"quantity"`
		ProrationFactor string `json:"proration_factor"`
		Amount          string `json:"amount"`
	}
	var lines []line
	for _, tr := range res.Traces {
		lines = append(lines, line{
			MeterCode: tr.MeterCode, Model: string(tr.PriceModel), RawEventCount: tr.RawEventCount,
			Quantity: tr.MeterValue.String(), ProrationFactor: tr.ProrationFactor.String(),
			Amount: money.Format(tr.Amount, res.Invoice.Currency),
		})
	}
	return map[string]any{
		"customer_id":  res.Invoice.CustomerID,
		"period_start": res.Invoice.PeriodStart,
		"period_end":   res.Invoice.PeriodEnd,
		"lines":        lines,
		"total":        money.Format(res.Invoice.Total, res.Invoice.Currency),
		"currency":     res.Invoice.Currency,
		"hash":         res.Hash, // verification hash; basis for the reconciliation ledger
	}
}

func parseDecOr0(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
