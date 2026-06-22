package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/alerts"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/reconcile"
)

// tool is one MCP tool: its advertised schema plus the handler that runs it.
type tool struct {
	name        string
	description string
	inputSchema map[string]any
	handler     func(ctx context.Context, args json.RawMessage) (string, error)
}

// obj is a small helper for building JSON Schema objects.
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// buildTools defines the intent-only surface. There is deliberately no
// charge()/calculate_bill() — the engine owns the math.
func (s *Server) buildTools() []tool {
	return []tool{
		{
			name:        "create_meter",
			description: "Define a usage meter. The agent sets the rule; smolbill aggregates raw events into a billable quantity.",
			inputSchema: obj(map[string]any{
				"code":         str("unique meter code, e.g. 'tokens'"),
				"name":         str("human-readable name"),
				"aggregation":  map[string]any{"type": "string", "enum": []string{"count", "sum", "max", "unique"}},
				"property_key": str("event property to aggregate (required unless aggregation=count)"),
			}, "code", "aggregation"),
			handler: s.createMeter,
		},
		{
			name:        "create_plan",
			description: "Create a versioned plan with one or more prices (flat | per_unit | tiered_graduated | tiered_volume). Amounts are decimal strings; the engine does all money math.",
			inputSchema: obj(map[string]any{
				"name":    str("plan name"),
				"version": map[string]any{"type": "integer", "description": "optional; defaults to 1"},
				"prices": map[string]any{"type": "array", "description": "list of prices", "items": obj(map[string]any{
					"model":       map[string]any{"type": "string", "enum": []string{"flat", "per_unit", "tiered_graduated", "tiered_volume"}},
					"currency":    str("ISO-4217, e.g. USD"),
					"meter_code":  str("meter this price bills (empty for a flat fee)"),
					"unit_amount": str("per-unit price as a decimal string"),
					"flat_amount": str("flat fee as a decimal string"),
					"tiers": map[string]any{"type": "array", "items": obj(map[string]any{
						"up_to":       str("inclusive upper bound; omit/null for the final unbounded tier"),
						"unit_amount": str("per-unit price within this tier"),
						"flat_amount": str("optional flat fee for this tier"),
					})},
				})},
			}, "name", "prices"),
			handler: s.createPlan,
		},
		{
			name:        "attach_plan",
			description: "Attach a plan to a customer (creates a subscription). Defaults the billing period to the current calendar month if not given.",
			inputSchema: obj(map[string]any{
				"customer_id":  str("customer id"),
				"plan_id":      str("plan id"),
				"period_start": str("optional RFC3339 period start"),
				"period_end":   str("optional RFC3339 period end"),
			}, "customer_id", "plan_id"),
			handler: s.attachPlan,
		},
		{
			name:        "set_spend_cap",
			description: "Set a spend cap for a customer: alerts fire (50/80/100% by default) as projected spend approaches the limit. This warns proactively; it does not hard-block usage.",
			inputSchema: obj(map[string]any{
				"customer_id": str("customer id"),
				"limit":       str("budget as a decimal string, e.g. '500.00'"),
				"currency":    str("ISO-4217; defaults to USD"),
				"webhook_url": str("URL to POST alerts to when thresholds are crossed"),
			}, "customer_id", "limit", "webhook_url"),
			handler: s.setSpendCap,
		},
		{
			name:        "get_usage",
			description: "Get a customer's current-period usage and projected bill (computed deterministically by the engine).",
			inputSchema: obj(map[string]any{"customer_id": str("customer id")}, "customer_id"),
			handler:     s.getUsage,
		},
		{
			name:        "preview_invoice",
			description: "Preview the exact current-period invoice for a customer, including the verification hash. No money is moved; the engine computes every cent.",
			inputSchema: obj(map[string]any{"customer_id": str("customer id")}, "customer_id"),
			handler:     s.previewInvoice,
		},
		{
			name:        "simulate_plan_change",
			description: "Sandbox: replay a customer's REAL usage this period against a PROPOSED plan and diff it against their live bill. Nothing is committed and no money moves — this is how you prove a pricing change is safe before applying it. The same deterministic engine that finalizes invoices computes the preview.",
			inputSchema: obj(map[string]any{
				"customer_id": str("customer id"),
				"plan": map[string]any{
					"type":        "object",
					"description": "proposed plan: {name, version, prices:[{meter_code, model(flat|per_unit|tiered_graduated|tiered_volume), currency, unit_amount, flat_amount, tiers:[{up_to, unit_amount, flat_amount}]}]}",
				},
			}, "customer_id", "plan"),
			handler: s.simulatePlanChange,
		},
		{
			name:        "create_customer",
			description: "Create a customer (the billed entity). Returns the customer id you then attach plans to.",
			inputSchema: obj(map[string]any{
				"name":        str("customer name"),
				"external_id": str("optional id in your own system"),
			}, "name"),
			handler: s.createCustomerTool,
		},
		{
			name:        "list_customers",
			description: "List all customers (id + name), so you can see who exists before acting.",
			inputSchema: obj(map[string]any{}),
			handler:     s.listCustomersTool,
		},
		{
			name:        "finalize_invoice",
			description: "Materialize a customer's current-period invoice for a subscription: compute it deterministically and persist it with a reconciliation ledger. No money is pushed to a processor (finalize over MCP is local) — the deterministic engine does every cent.",
			inputSchema: obj(map[string]any{"subscription_id": str("subscription id")}, "subscription_id"),
			handler:     s.finalizeInvoiceTool,
		},
		{
			name:        "reconcile_invoice",
			description: "Prove a finalized invoice still matches the live event log. Returns 'consistent' or the exact line-level drift if a late/out-of-order event changed the bill after finalize.",
			inputSchema: obj(map[string]any{"invoice_id": str("invoice id")}, "invoice_id"),
			handler:     s.reconcileInvoiceTool,
		},
		{
			name:        "get_analytics",
			description: "Account-wide snapshot: customer count, active subscriptions, projected + finalized revenue by currency, and dunning recovery (at-risk, recovered, recovery rate). Computed live, never a cached counter.",
			inputSchema: obj(map[string]any{}),
			handler:     s.getAnalyticsTool,
		},
		{
			name:        "create_webhook",
			description: "Register an endpoint to receive signed lifecycle events (invoice.finalized, drift.detected, payment_failed, recovered, uncollectible). Returns a signing secret shown only once.",
			inputSchema: obj(map[string]any{
				"url":    str("HTTPS endpoint to POST events to"),
				"events": map[string]any{"type": "array", "items": str("event type; omit for all events")},
			}, "url"),
			handler: s.createWebhookTool,
		},
		{
			name:        "get_collection",
			description: "Inspect the dunning/recovery state of an invoice: status, attempts made, the last decline reason, and when the next retry is due.",
			inputSchema: obj(map[string]any{"invoice_id": str("invoice id")}, "invoice_id"),
			handler:     s.getCollectionTool,
		},
	}
}

// --- additional intent/observe handlers (full-lifecycle MCP surface) ---

func (s *Server) createCustomerTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct{ Name, ExternalID string }
	if err := decodeArgs(args, &a, map[string]*string{"name": &a.Name, "external_id": &a.ExternalID}); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	c := domain.Customer{ID: id.New("cus"), Name: a.Name, ExternalID: a.ExternalID, CreatedAt: s.now()}
	if err := s.store.PutCustomer(c); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created customer %q (id %s). Attach a plan to it next.", a.Name, c.ID), nil
}

func (s *Server) listCustomersTool(_ context.Context, _ json.RawMessage) (string, error) {
	cs, err := s.store.ListCustomers()
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "No customers yet. Create one with create_customer.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d customer(s):\n", len(cs))
	for _, c := range cs {
		name := c.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&b, "  • %s — %s\n", c.ID, name)
	}
	return b.String(), nil
}

func (s *Server) finalizeInvoiceTool(_ context.Context, args json.RawMessage) (string, error) {
	var subID string
	if err := json.Unmarshal(args, &struct {
		SubscriptionID *string `json:"subscription_id"`
	}{&subID}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if subID == "" {
		return "", fmt.Errorf("subscription_id is required")
	}
	sub, ok, err := s.store.GetSubscription(subID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown subscription_id %q", subID)
	}
	res, err := engine.Compute(s.store, sub)
	if err != nil {
		return "", err
	}
	inv := res.Invoice
	inv.ID = id.New("inv")
	inv.Status = "finalized"
	ledger := reconcile.LedgerFromResult(inv.ID, res)
	if err := s.store.SaveFinalizedInvoice(inv, ledger); err != nil {
		return "", err
	}
	return fmt.Sprintf("Finalized invoice %s for customer %s: $%s %s (period %s → %s). Reconciliation ledger persisted — call reconcile_invoice any time. No money pushed (finalize over MCP is local).",
		inv.ID, inv.CustomerID, inv.Total.StringFixed(2), inv.Currency,
		inv.PeriodStart.Format("2006-01-02"), inv.PeriodEnd.Format("2006-01-02")), nil
}

func (s *Server) reconcileInvoiceTool(_ context.Context, args json.RawMessage) (string, error) {
	var invID string
	if err := json.Unmarshal(args, &struct {
		InvoiceID *string `json:"invoice_id"`
	}{&invID}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if invID == "" {
		return "", fmt.Errorf("invoice_id is required")
	}
	inv, ok, err := s.store.GetInvoice(invID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown invoice_id %q", invID)
	}
	ledger, err := s.store.GetLedger(invID)
	if err != nil {
		return "", err
	}
	sub, ok, err := s.store.GetSubscription(inv.SubscriptionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("the subscription for this invoice no longer exists")
	}
	// Pin the recompute to the invoice's own period so we compare like with like.
	sub.CurrentPeriodStart = inv.PeriodStart
	sub.CurrentPeriodEnd = inv.PeriodEnd
	live, err := engine.Compute(s.store, sub)
	if err != nil {
		return "", err
	}
	proof := reconcile.Build(inv, ledger, live)
	if proof.Consistent {
		return fmt.Sprintf("Invoice %s reconciles: the meter and the invoice provably agree (total $%s, hash match). Nothing drifted.",
			inv.ID, proof.StoredTotal), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DRIFT on invoice %s: stored $%s vs live $%s.\n", inv.ID, proof.StoredTotal, proof.LiveTotal)
	for _, d := range proof.Diffs {
		fmt.Fprintf(&b, "  • %s\n", d)
	}
	for _, l := range proof.Lines {
		for _, d := range l.Diffs {
			fmt.Fprintf(&b, "  • [%s] %s\n", l.MeterCode, d)
		}
	}
	return b.String(), nil
}

func (s *Server) getAnalyticsTool(_ context.Context, _ json.RawMessage) (string, error) {
	a, err := engine.ComputeAnalytics(s.store, s.now())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Account snapshot (%s):\n", a.GeneratedAt.Format("2006-01-02"))
	fmt.Fprintf(&b, "  customers: %d · active subscriptions: %d · finalized invoices: %d\n",
		a.Customers, a.ActiveSubscriptions, a.FinalizedInvoices)
	for cur, amt := range a.ProjectedRevenue {
		fmt.Fprintf(&b, "  projected this period: %s %s\n", amt, cur)
	}
	for cur, amt := range a.FinalizedRevenue {
		fmt.Fprintf(&b, "  finalized revenue: %s %s\n", amt, cur)
	}
	fmt.Fprintf(&b, "  dunning: %d retrying · %d need action · %d recovered · %d written off (recovery rate %s)\n",
		a.Dunning.Retrying, a.Dunning.RequiresAction, a.Dunning.Recovered, a.Dunning.Uncollectible, a.Dunning.RecoveryRate)
	for cur, amt := range a.Dunning.AmountAtRisk {
		fmt.Fprintf(&b, "  at risk: %s %s\n", amt, cur)
	}
	return b.String(), nil
}

func (s *Server) createWebhookTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	wh := domain.Webhook{ID: id.New("wh"), URL: a.URL, Events: a.Events, Secret: id.New("whsec"), CreatedAt: s.now()}
	if err := s.store.PutWebhook(wh); err != nil {
		return "", err
	}
	return fmt.Sprintf("Registered webhook %s → %s. Signing secret (shown only once): %s", wh.ID, wh.URL, wh.Secret), nil
}

func (s *Server) getCollectionTool(_ context.Context, args json.RawMessage) (string, error) {
	var invID string
	if err := json.Unmarshal(args, &struct {
		InvoiceID *string `json:"invoice_id"`
	}{&invID}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	col, ok, err := s.store.GetCollection(invID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no collection for invoice %q (it may be paid, or never pushed to a processor)", invID)
	}
	msg := fmt.Sprintf("Collection for %s: status %s, %d attempt(s)", col.InvoiceID, col.Status, col.Attempts)
	if col.LastReason != "" {
		msg += fmt.Sprintf(", last failure: %s", col.LastReason)
	}
	if col.NextAttemptAt != nil {
		msg += fmt.Sprintf(", next retry due %s", col.NextAttemptAt.Format(time.RFC3339))
	}
	return msg + ".", nil
}

// --- handlers (intent in; the engine/store does the work) ---

func (s *Server) createMeter(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Code, Name, Aggregation, PropertyKey string
	}
	if err := decodeArgs(args, &a, map[string]*string{
		"code": &a.Code, "name": &a.Name, "aggregation": &a.Aggregation, "property_key": &a.PropertyKey,
	}); err != nil {
		return "", err
	}
	agg := domain.Aggregation(a.Aggregation)
	switch agg {
	case domain.AggCount, domain.AggSum, domain.AggMax, domain.AggUnique:
	default:
		return "", fmt.Errorf("aggregation must be count|sum|max|unique")
	}
	if a.Code == "" {
		return "", fmt.Errorf("code is required")
	}
	if agg != domain.AggCount && a.PropertyKey == "" {
		return "", fmt.Errorf("property_key is required for aggregation %q", a.Aggregation)
	}
	m := domain.Meter{ID: id.New("mtr"), Code: a.Code, Name: a.Name, Aggregation: agg, PropertyKey: a.PropertyKey, CreatedAt: s.now()}
	if err := s.store.PutMeter(m); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created meter %q (%s). It now aggregates incoming events into a billable quantity.", a.Code, a.Aggregation), nil
}

func (s *Server) createPlan(_ context.Context, args json.RawMessage) (string, error) {
	var in engine.PlanInput
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	plan, err := engine.BuildPlan(in)
	if err != nil {
		return "", err
	}
	plan.CreatedAt = s.now()
	if err := s.store.PutPlan(plan); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created plan %q v%d (id %s) with %d price(s).", plan.Name, plan.Version, plan.ID, len(plan.Prices)), nil
}

func (s *Server) attachPlan(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID, PlanID, PeriodStart, PeriodEnd string
	}
	if err := json.Unmarshal(args, &struct {
		CustomerID  *string `json:"customer_id"`
		PlanID      *string `json:"plan_id"`
		PeriodStart *string `json:"period_start"`
		PeriodEnd   *string `json:"period_end"`
	}{&a.CustomerID, &a.PlanID, &a.PeriodStart, &a.PeriodEnd}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if _, ok, _ := s.store.GetCustomer(a.CustomerID); !ok {
		return "", fmt.Errorf("unknown customer_id %q", a.CustomerID)
	}
	plan, ok, err := s.store.GetPlan(a.PlanID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown plan_id %q", a.PlanID)
	}

	start, end, err := s.resolvePeriod(a.PeriodStart, a.PeriodEnd)
	if err != nil {
		return "", err
	}
	sub := domain.Subscription{
		ID: id.New("sub"), CustomerID: a.CustomerID, PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: start, CurrentPeriodEnd: end, StartedAt: start,
	}
	if err := s.store.PutSubscription(sub); err != nil {
		return "", err
	}
	return fmt.Sprintf("Attached plan %q to customer %s for %s → %s (subscription %s).",
		plan.Name, a.CustomerID, start.Format("2006-01-02"), end.Format("2006-01-02"), sub.ID), nil
}

func (s *Server) setSpendCap(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID, Limit, Currency, WebhookURL string
	}
	_ = json.Unmarshal(args, &struct {
		CustomerID *string `json:"customer_id"`
		Limit      *string `json:"limit"`
		Currency   *string `json:"currency"`
		WebhookURL *string `json:"webhook_url"`
	}{&a.CustomerID, &a.Limit, &a.Currency, &a.WebhookURL})

	if _, ok, _ := s.store.GetCustomer(a.CustomerID); !ok {
		return "", fmt.Errorf("unknown customer_id %q", a.CustomerID)
	}
	limit, err := decimal.NewFromString(a.Limit)
	if err != nil || !limit.IsPositive() {
		return "", fmt.Errorf("limit must be a positive decimal string")
	}
	if a.WebhookURL == "" {
		return "", fmt.Errorf("webhook_url is required so the cap can alert you")
	}
	currency := a.Currency
	if currency == "" {
		currency = "USD"
	}
	alert := domain.Alert{
		ID: id.New("alert"), CustomerID: a.CustomerID, Budget: limit, Currency: currency,
		Thresholds: alerts.DefaultThresholds, WebhookURL: a.WebhookURL,
	}
	if err := s.store.PutAlert(alert); err != nil {
		return "", err
	}
	return fmt.Sprintf("Spend cap set for customer %s at %s %s. Alerts fire at 50/80/100%% of budget (proactive warning, not a hard block).",
		a.CustomerID, limit.StringFixed(2), currency), nil
}

func (s *Server) getUsage(_ context.Context, args json.RawMessage) (string, error) {
	cid, err := singleCustomerID(args)
	if err != nil {
		return "", err
	}
	res, sub, err := engine.ComputeForActiveSub(s.store, cid)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Usage for %s (period %s → %s):\n", cid,
		sub.CurrentPeriodStart.Format("2006-01-02"), sub.CurrentPeriodEnd.Format("2006-01-02"))
	for _, tr := range res.Traces {
		code := tr.MeterCode
		if code == "" {
			code = "(base fee)"
		}
		fmt.Fprintf(&b, "  • %s: %s → $%s\n", code, tr.MeterValue.String(), tr.Amount.StringFixed(2))
	}
	fmt.Fprintf(&b, "Projected bill: $%s %s", res.Invoice.Total.StringFixed(2), res.Invoice.Currency)
	return b.String(), nil
}

func (s *Server) previewInvoice(_ context.Context, args json.RawMessage) (string, error) {
	cid, err := singleCustomerID(args)
	if err != nil {
		return "", err
	}
	res, _, err := engine.ComputeForActiveSub(s.store, cid)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Invoice preview for %s — period %s → %s\n",
		cid, res.Invoice.PeriodStart.Format("2006-01-02"), res.Invoice.PeriodEnd.Format("2006-01-02"))
	for _, l := range res.Invoice.Lines {
		code := l.MeterCode
		if code == "" {
			code = "(base fee)"
		}
		fmt.Fprintf(&b, "  • %s — qty %s — $%s\n", code, l.Quantity.String(), l.Amount.StringFixed(2))
	}
	fmt.Fprintf(&b, "Total: $%s %s\nVerification hash: %s\n(No money moved — this is a deterministic preview.)",
		res.Invoice.Total.StringFixed(2), res.Invoice.Currency, res.Hash)
	return b.String(), nil
}

func (s *Server) simulatePlanChange(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID string           `json:"customer_id"`
		Plan       engine.PlanInput `json:"plan"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	res, err := engine.SimulatePlanChange(s.store, a.CustomerID, a.Plan)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Simulation for %s — period %s → %s (nothing committed)\n",
		res.CustomerID, res.PeriodStart.Format("2006-01-02"), res.PeriodEnd.Format("2006-01-02"))
	for _, l := range res.Lines {
		code := l.MeterCode
		if code == "" {
			code = "(base fee)"
		}
		fmt.Fprintf(&b, "  • %s — now $%s, proposed $%s (delta $%s)\n",
			code, l.CurrentAmount.StringFixed(2), l.ProposedAmount.StringFixed(2), l.Delta.StringFixed(2))
	}
	fmt.Fprintf(&b, "Current total $%s -> proposed $%s, a change of $%s %s\n(No money moved, no config changed — this is a deterministic sandbox preview.)",
		res.CurrentTotal.StringFixed(2), res.ProposedTotal.StringFixed(2), res.Delta.StringFixed(2), res.Currency)
	return b.String(), nil
}

// --- arg helpers ---

// resolvePeriod returns the billing window, defaulting to the current calendar
// month when start/end are omitted.
func (s *Server) resolvePeriod(startStr, endStr string) (time.Time, time.Time, error) {
	if startStr == "" && endStr == "" {
		now := s.now()
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period_start must be RFC3339")
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period_end must be RFC3339")
	}
	return start, end, nil
}

func singleCustomerID(args json.RawMessage) (string, error) {
	var a struct {
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.CustomerID == "" {
		return "", fmt.Errorf("customer_id is required")
	}
	return a.CustomerID, nil
}

// decodeArgs unmarshals JSON object args into the provided string fields by key.
func decodeArgs(args json.RawMessage, _ any, fields map[string]*string) error {
	raw := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
	}
	for k, ptr := range fields {
		if v, ok := raw[k]; ok {
			if sv, ok := v.(string); ok {
				*ptr = sv
			}
		}
	}
	return nil
}
