package api

import (
	"net/http"
)

// --- POST /v1/wallet/{customer_id}/topup ---

type topupReq struct {
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) topupWallet(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("customer_id")
	var req topupReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok, _ := s.store.GetCustomer(cid); !ok {
		writeErr(w, http.StatusBadRequest, "unknown customer_id")
		return
	}
	amount := parseDecOr0(req.Amount)
	if !amount.IsPositive() {
		writeErr(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}
	wlt, err := s.store.TopUpWallet(cid, amount, currency, req.Reason, req.IdempotencyKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer_id": cid,
		"balance":     wlt.Balance.StringFixed(2),
		"currency":    wlt.Currency,
	})
}

// --- GET /v1/wallet/{customer_id} ---

func (s *Server) getWallet(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("customer_id")
	wlt, ok, err := s.store.Wallet(cid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"customer_id": cid, "balance": "0.00", "currency": "USD"})
		return
	}
	txns, _ := s.store.WalletTransactions(cid)
	type txOut struct {
		Amount string `json:"amount"`
		Reason string `json:"reason"`
		At     string `json:"at"`
	}
	var out []txOut
	for _, t := range txns {
		out = append(out, txOut{t.Amount.StringFixed(2), t.Reason, t.CreatedAt.Format("2006-01-02T15:04:05Z07:00")})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer_id":  cid,
		"balance":      wlt.Balance.StringFixed(2),
		"currency":     wlt.Currency,
		"transactions": out,
	})
}
