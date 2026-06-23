package api

import "net/http"

// Read-only list endpoints (§dashboard reads). These let an external dashboard
// read the full surface — customers, plans, subscriptions, invoices. No money
// math lives here; each is a thin wrapper over the store returning all rows.

func (s *Server) listCustomers(w http.ResponseWriter, _ *http.Request) {
	customers, err := s.store.ListCustomers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": customers})
}

func (s *Server) listPlans(w http.ResponseWriter, _ *http.Request) {
	plans, err := s.store.ListPlans()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (s *Server) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (s *Server) listInvoices(w http.ResponseWriter, _ *http.Request) {
	invoices, err := s.store.ListInvoices()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoices": invoices})
}
