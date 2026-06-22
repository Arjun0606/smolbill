package api

import (
	"context"
	"log"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/alerts"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/id"
)

// --- POST /v1/alerts ---
//
// Registers a proactive spend alert: when the customer's projected current-period
// spend crosses each threshold of the budget, smolbill POSTs to the webhook —
// before the overage (build plan §9). Evaluation happens automatically on every
// event ingest.

type alertReq struct {
	CustomerID string `json:"customer_id"`
	Budget     string `json:"budget"`
	Currency   string `json:"currency"`
	WebhookURL string `json:"webhook_url"`
	Thresholds []int  `json:"thresholds"` // optional; defaults to 50/80/100
}

func (s *Server) createAlert(w http.ResponseWriter, r *http.Request) {
	var req alertReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CustomerID == "" || req.WebhookURL == "" {
		writeErr(w, http.StatusBadRequest, "customer_id and webhook_url are required")
		return
	}
	if _, ok, _ := s.store.GetCustomer(req.CustomerID); !ok {
		writeErr(w, http.StatusBadRequest, "unknown customer_id")
		return
	}
	budget := parseDecOr0(req.Budget)
	if !budget.IsPositive() {
		writeErr(w, http.StatusBadRequest, "budget must be positive")
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}
	thresholds := req.Thresholds
	if len(thresholds) == 0 {
		thresholds = alerts.DefaultThresholds
	}
	a := domain.Alert{
		ID: id.New("alert"), CustomerID: req.CustomerID, Budget: budget,
		Currency: currency, Thresholds: thresholds, WebhookURL: req.WebhookURL,
	}
	if err := s.store.PutAlert(a); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// evaluateAlerts is called after each accepted event. It projects the customer's
// current-period spend and fires any newly-crossed thresholds. Best-effort: an
// alert/webhook failure is logged but never fails the ingest (the event is
// already safely recorded).
func (s *Server) evaluateAlerts(ctx context.Context, customerID string) {
	configs, err := s.store.AlertsForCustomer(customerID)
	if err != nil || len(configs) == 0 {
		return
	}
	res, _, err := s.computeForActiveSub(customerID)
	if err != nil {
		return // no active subscription to project against yet
	}
	projected := res.Invoice.Total

	for _, a := range configs {
		if a.Currency != res.Invoice.Currency || !a.Budget.IsPositive() {
			continue
		}
		pct := projected.Div(a.Budget).Mul(decimal.NewFromInt(100))
		toFire, newMax := alerts.Crossed(a.MaxFired, pct, a.Thresholds)
		for _, t := range toFire {
			n := alerts.Notification{
				CustomerID: customerID, Threshold: t,
				Budget: a.Budget.StringFixed(2), Spent: projected.StringFixed(2),
				PctUsed: pct.StringFixed(1), Currency: a.Currency,
				FiredAt: s.now().UTC().Format("2006-01-02T15:04:05Z07:00"),
			}
			if err := s.notifier.Notify(ctx, a.WebhookURL, n); err != nil {
				log.Printf("smolbill: alert webhook failed (customer=%s threshold=%d): %v", customerID, t, err)
			}
		}
		if newMax != a.MaxFired {
			if err := s.store.UpdateAlertFired(a.ID, newMax); err != nil {
				log.Printf("smolbill: update alert fired mark failed (alert=%s): %v", a.ID, err)
			}
		}
	}
}
