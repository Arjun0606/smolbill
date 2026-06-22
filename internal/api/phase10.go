package api

import (
	"net/http"

	"github.com/Arjun0606/smolbill/internal/comms"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/money"
)

// --- dunning message templates (Phase 10) ---
//
// The customer-facing copy for failed-payment recovery is yours to read, edit,
// and preview — the opposite of a merchant-of-record that hides the dunning
// emails. Defaults are built in; an override is stored per event. Every dunning
// webhook carries the rendered subject+body so your ESP/SMS just sends it.
//
//   GET  /v1/dunning/templates           — see the effective copy for every event
//   PUT  /v1/dunning/templates/{event}   — override one event's copy
//   POST /v1/dunning/preview             — render a message (the live preview)

// loadTemplates returns the operator's overrides keyed by comms.Event.
func (s *Server) loadTemplates() map[comms.Event]comms.Template {
	out := map[comms.Event]comms.Template{}
	tmpls, err := s.store.MessageTemplates()
	if err != nil {
		return out
	}
	for _, t := range tmpls {
		out[comms.Event(t.Event)] = comms.Template{Subject: t.Subject, Body: t.Body}
	}
	return out
}

func (s *Server) listDunningTemplates(w http.ResponseWriter, r *http.Request) {
	overrides := s.loadTemplates()
	type tmplOut struct {
		Event      string `json:"event"`
		Subject    string `json:"subject"`
		Body       string `json:"body"`
		Customized bool   `json:"customized"`
	}
	out := make([]tmplOut, 0, len(comms.AllEvents))
	for _, e := range comms.AllEvents {
		t := comms.For(e, overrides)
		_, custom := overrides[e]
		out = append(out, tmplOut{Event: string(e), Subject: t.Subject, Body: t.Body, Customized: custom})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

type dunningTemplateReq struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (s *Server) setDunningTemplate(w http.ResponseWriter, r *http.Request) {
	event := r.PathValue("event")
	if !validDunningEvent(event) {
		writeErr(w, http.StatusBadRequest, "unknown event; one of payment_failed|requires_action|recovered|uncollectible")
		return
	}
	var req dunningTemplateReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Subject == "" || req.Body == "" {
		writeErr(w, http.StatusBadRequest, "subject and body are required")
		return
	}
	// Validate the template renders before saving — never store copy that would
	// fail at delivery time.
	if _, err := comms.Render(comms.Template{Subject: req.Subject, Body: req.Body}, sampleCommsContext()); err != nil {
		writeErr(w, http.StatusBadRequest, "template does not render: "+err.Error())
		return
	}
	if err := s.store.PutMessageTemplate(domain.MessageTemplate{
		Event: event, Subject: req.Subject, Body: req.Body, UpdatedAt: s.now(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": event, "customized": true})
}

type dunningPreviewReq struct {
	Event   string         `json:"event"`
	Context *comms.Context `json:"context"` // optional; defaults to a sample
}

func (s *Server) previewDunningMessage(w http.ResponseWriter, r *http.Request) {
	var req dunningPreviewReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validDunningEvent(req.Event) {
		writeErr(w, http.StatusBadRequest, "unknown event; one of payment_failed|requires_action|recovered|uncollectible")
		return
	}
	ctx := sampleCommsContext()
	if req.Context != nil {
		ctx = *req.Context
	}
	msg, err := comms.Render(comms.For(comms.Event(req.Event), s.loadTemplates()), ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": req.Event, "subject": msg.Subject, "body": msg.Body})
}

func validDunningEvent(e string) bool {
	for _, a := range comms.AllEvents {
		if string(a) == e {
			return true
		}
	}
	return false
}

func sampleCommsContext() comms.Context {
	return comms.Context{
		CustomerName: "Acme Inc", InvoiceID: "inv_demo", Amount: "64.00", Currency: "USD",
		Attempts: 2, NextAttempt: "2026-06-25", Reason: "insufficient_funds", UpdateURL: "https://your-app.com/billing",
	}
}

// renderDunningMessage builds the rendered customer message for a collection's
// current state, so the dunning webhook can carry ready-to-send copy. Best-effort:
// returns ("", "") if the event isn't one we message on.
func (s *Server) renderDunningMessage(event comms.Event, col domain.Collection) comms.Message {
	name := "there"
	if inv, ok, _ := s.store.GetInvoice(col.InvoiceID); ok {
		if cust, ok, _ := s.store.GetCustomer(inv.CustomerID); ok && cust.Name != "" {
			name = cust.Name
		}
	}
	next := ""
	if col.NextAttemptAt != nil {
		next = col.NextAttemptAt.Format("2006-01-02")
	}
	ctx := comms.Context{
		CustomerName: name, InvoiceID: col.InvoiceID,
		Amount:   money.FromMinor(col.AmountMinor, col.Currency).Amount(),
		Currency: col.Currency, Attempts: col.Attempts, NextAttempt: next, Reason: col.LastReason,
	}
	msg, _ := comms.Render(comms.For(event, s.loadTemplates()), ctx)
	return msg
}
