package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/engine"
)

// gate is the real-time enforcement endpoint: POST a customer + a feature and/or a
// cost, get back a synchronous allow/deny computed live. Call it in your request hot
// path before serving an action — it HARD-BLOCKS at the entitlement limit or at
// balance=0, the request-level enforcement even Metronome only does as alerts.
//
// A denied gate is a valid answer (HTTP 200 with allowed=false), not an error.
func (s *Server) gate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomerID string          `json:"customer_id"`
		Feature    string          `json:"feature"`
		Quantity   decimal.Decimal `json:"quantity"`
		Cost       decimal.Decimal `json:"cost"`
		Currency   string          `json:"currency"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.CustomerID == "" {
		writeErr(w, http.StatusBadRequest, "customer_id is required")
		return
	}
	d, err := engine.Gate(s.store, engine.GateRequest{
		CustomerID: req.CustomerID,
		Feature:    req.Feature,
		Quantity:   req.Quantity,
		Cost:       req.Cost,
		Currency:   req.Currency,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}
