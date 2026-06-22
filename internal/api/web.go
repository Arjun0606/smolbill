package api

import (
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/meter"
	"github.com/Arjun0606/smolbill/internal/reconcile"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates is parsed once at startup. html/template auto-escapes, so customer
// data rendered into pages is safe by construction.
var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"money":    func(d decimal.Decimal) string { return d.StringFixed(2) },
	"date":     func(t time.Time) string { return t.Format("2006-01-02") },
	"datetime": func(t time.Time) string { return t.Format("2006-01-02 15:04") },
}).ParseFS(templateFS, "templates/*.html"))

func (s *Server) renderHTML(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// --- dashboard: customer index ---

func (s *Server) dashboardHome(w http.ResponseWriter, _ *http.Request) {
	customers, err := s.store.ListCustomers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderHTML(w, "dashboard.html", map[string]any{"Customers": customers})
}

// --- dashboard: customer detail ---

type usageLine struct {
	MeterCode string
	Quantity  string
	Amount    string
}

func (s *Server) dashboardCustomer(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("customer_id")
	cust, ok, err := s.store.GetCustomer(cid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown customer", http.StatusNotFound)
		return
	}

	vm := map[string]any{"Customer": cust, "HasSub": false}

	if res, sub, err := s.computeForActiveSub(cid); err == nil {
		vm["HasSub"] = true
		vm["Subscription"] = sub
		vm["Projected"] = res.Invoice.Total.StringFixed(2)
		vm["Currency"] = res.Invoice.Currency
		var lines []usageLine
		for _, tr := range res.Traces {
			code := tr.MeterCode
			if code == "" {
				code = "(base fee)"
			}
			lines = append(lines, usageLine{code, tr.MeterValue.String(), tr.Amount.StringFixed(2)})
		}
		vm["Usage"] = lines
	}

	if wlt, ok, _ := s.store.Wallet(cid); ok {
		vm["Wallet"] = wlt
	}
	if txns, err := s.store.WalletTransactions(cid); err == nil {
		vm["WalletTxns"] = txns
	}
	if invs, err := s.store.InvoicesForCustomer(cid); err == nil {
		vm["Invoices"] = invs
	}
	if events, err := s.store.EventsForCustomer(cid); err == nil {
		vm["Events"] = recentEvents(events, 20)
	}
	s.renderHTML(w, "customer.html", vm)
}

// recentEvents returns the most recent n events (by event_time), newest first.
func recentEvents(events []domain.Event, n int) []domain.Event {
	// events come oldest-first from the store; reverse and cap.
	out := make([]domain.Event, 0, len(events))
	for i := len(events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, events[i])
	}
	return out
}

// --- dashboard: reconciliation ledger view ---

func (s *Server) dashboardReconcile(w http.ResponseWriter, r *http.Request) {
	invID := r.PathValue("invoice_id")
	storedInv, ok, err := s.store.GetInvoice(invID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown invoice", http.StatusNotFound)
		return
	}
	ledger, err := s.store.GetLedger(invID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	live, err := s.recomputeForInvoice(storedInv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proof := reconcile.Build(storedInv, ledger, live)
	s.renderHTML(w, "reconcile.html", map[string]any{"Invoice": storedInv, "Proof": proof})
}

// --- embeddable customer portal ---

func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("customer_id")
	cust, ok, err := s.store.GetCustomer(cid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown customer", http.StatusNotFound)
		return
	}
	vm := map[string]any{"Customer": cust, "HasSub": false}

	if res, sub, err := s.computeForActiveSub(cid); err == nil {
		vm["HasSub"] = true
		vm["Subscription"] = sub
		vm["Projected"] = res.Invoice.Total.StringFixed(2)
		vm["Currency"] = res.Invoice.Currency
		var lines []usageLine
		for _, tr := range res.Traces {
			code := tr.MeterCode
			if code == "" {
				code = "Subscription"
			}
			lines = append(lines, usageLine{code, tr.MeterValue.String(), tr.Amount.StringFixed(2)})
		}
		vm["Usage"] = lines
	}
	if wlt, ok, _ := s.store.Wallet(cid); ok {
		vm["Wallet"] = wlt
	}
	vm["Entitlements"] = s.liveEntitlements(cid)
	s.renderHTML(w, "portal.html", vm)
}

// liveEntitlements computes current usage vs limit for the portal (mirrors the
// JSON endpoint, shaped for the template).
type portalEnt struct {
	Feature     string
	Used        string
	Limit       string
	PctUsed     string
	WithinLimit bool
}

func (s *Server) liveEntitlements(cid string) []portalEnt {
	ents, err := s.store.EntitlementsForCustomer(cid)
	if err != nil || len(ents) == 0 {
		return nil
	}
	meters, _ := s.store.Meters()
	events, _ := s.store.EventsForCustomer(cid)
	var out []portalEnt
	for _, e := range ents {
		pe := portalEnt{Feature: e.Feature, WithinLimit: true}
		if e.Kind == domain.EntMetered {
			used := decimal.Zero
			if m, ok := meters[e.MeterCode]; ok {
				used, _ = meter.Aggregate(m, events, e.PeriodStart, e.PeriodEnd)
			}
			pe.Used = used.String()
			pe.Limit = e.LimitValue.String()
			pe.WithinLimit = used.LessThanOrEqual(e.LimitValue)
			if e.LimitValue.IsPositive() {
				pe.PctUsed = used.Div(e.LimitValue).Mul(decimal.NewFromInt(100)).StringFixed(0)
			}
		}
		out = append(out, pe)
	}
	return out
}
