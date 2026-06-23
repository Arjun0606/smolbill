package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/alerts"
	"github.com/Arjun0606/smolbill/internal/comms"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/dunning"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/reconcile"
	"github.com/Arjun0606/smolbill/internal/revrec"
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
			description: "Get a customer's current-period usage and projected bill (computed deterministically by the engine). Pass as_of (RFC3339) to time-travel: see the bill as it was known at a past moment, before late events arrived.",
			inputSchema: obj(map[string]any{
				"customer_id": str("customer id"),
				"as_of":       str("optional RFC3339; recompute the bill as of this past moment"),
			}, "customer_id"),
			handler: s.getUsage,
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
			name:        "scan_billing_drift",
			description: "Scan the WHOLE account for billing drift: reconcile every finalized invoice against a fresh recompute from the live events and report how many drifted plus the total money at risk. This is the 'find revenue leakage' / 'prove all my invoices are correct' tool — answer 'is any of my billing wrong?' in one call, with the exact invoices and dollar amounts.",
			inputSchema: obj(map[string]any{}),
			handler:     s.scanDriftTool,
		},
		{
			name:        "get_revenue_recognition",
			description: "ASC 606 revenue recognition for a finalized invoice as of a date: how much is recognized revenue vs still deferred, straight-line over the service period. Optional as_of (RFC3339); defaults to now.",
			inputSchema: obj(map[string]any{"invoice_id": str("invoice id"), "as_of": str("RFC3339 date; defaults to now")}, "invoice_id"),
			handler:     s.revenueRecognitionTool,
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
		{
			name:        "record_usage",
			description: "Record a usage event (idempotent on idempotency_key — a repeat is never double-counted). High-volume ingestion normally comes from your app/SDK; the agent can record events for setup or testing.",
			inputSchema: obj(map[string]any{
				"idempotency_key": str("unique key; a repeat is ignored"),
				"customer_id":     str("customer id"),
				"meter_code":      str("meter this event feeds"),
				"properties":      map[string]any{"type": "object", "description": `event properties, e.g. {"n": 1000}`},
				"event_time":      str("optional RFC3339; defaults to now"),
			}, "idempotency_key", "customer_id", "meter_code"),
			handler: s.recordUsageTool,
		},
		{
			name:        "create_entitlement",
			description: "Define a feature entitlement for a customer: a boolean feature flag, or a metered allowance whose limit is checked live against the event log.",
			inputSchema: obj(map[string]any{
				"customer_id":  str("customer id"),
				"feature":      str("feature name, e.g. 'api_access'"),
				"kind":         map[string]any{"type": "string", "enum": []string{"boolean", "metered"}},
				"meter_code":   str("for metered: which meter measures usage"),
				"limit_value":  str("for metered: the allowance as a decimal string"),
				"period_start": str("optional RFC3339"),
				"period_end":   str("optional RFC3339"),
			}, "customer_id", "feature", "kind"),
			handler: s.createEntitlementTool,
		},
		{
			name:        "check_entitlement",
			description: "Check a customer's live entitlements: for each feature whether they're within limit, how much is used, and how much remains. Used value is derived from the event log, never a trusted counter.",
			inputSchema: obj(map[string]any{"customer_id": str("customer id")}, "customer_id"),
			handler:     s.checkEntitlementTool,
		},
		{
			name:        "gate_check",
			description: "Real-time enforcement: may this customer do this action RIGHT NOW? Returns a hard allow/deny computed live against a metered entitlement (feature + optional quantity) and/or the prepaid wallet (cost). Call before serving a request to block at the limit or at balance=0 — spend caps only warn; this denies.",
			inputSchema: obj(map[string]any{
				"customer_id": str("customer id"),
				"feature":     str("entitlement feature to check (optional)"),
				"quantity":    str("units requested against the feature limit; default 1"),
				"cost":        str("monetary cost to check against the prepaid wallet (optional)"),
				"currency":    str("currency for cost/wallet; default USD"),
			}, "customer_id"),
			handler: s.gateCheckTool,
		},
		{
			name:        "list_meters",
			description: "List all defined meters (code + aggregation).",
			inputSchema: obj(map[string]any{}),
			handler:     s.listMetersTool,
		},
		{
			name:        "list_plans",
			description: "List every plan (id, name, version, and a one-line pricing summary of each component), so you can see what pricing exists before attaching a customer or proposing a change.",
			inputSchema: obj(map[string]any{}),
			handler:     s.listPlansTool,
		},
		{
			name:        "list_subscriptions",
			description: "List every subscription (id, customer, plan, status, current period), so you know who is on what before finalizing invoices or changing plans.",
			inputSchema: obj(map[string]any{}),
			handler:     s.listSubscriptionsTool,
		},
		{
			name:        "list_invoices",
			description: "List every invoice (id, customer, period, total, status) across the account — the global ledger view, so you can find an invoice to reconcile, verify, or recognize revenue on.",
			inputSchema: obj(map[string]any{}),
			handler:     s.listInvoicesTool,
		},
		{
			name:        "list_webhooks",
			description: "List registered webhook endpoints (secrets are never shown).",
			inputSchema: obj(map[string]any{}),
			handler:     s.listWebhooksTool,
		},
		{
			name:        "topup_wallet",
			description: "Credit a customer's prepaid wallet (idempotent on idempotency_key).",
			inputSchema: obj(map[string]any{
				"customer_id":     str("customer id"),
				"amount":          str("amount to credit as a decimal string"),
				"currency":        str("ISO-4217; defaults to USD"),
				"reason":          str("optional note"),
				"idempotency_key": str("optional; makes the credit safely retryable"),
			}, "customer_id", "amount"),
			handler: s.topupWalletTool,
		},
		{
			name:        "get_wallet",
			description: "Get a customer's prepaid wallet balance.",
			inputSchema: obj(map[string]any{"customer_id": str("customer id")}, "customer_id"),
			handler:     s.getWalletTool,
		},
		{
			name:        "verify_invoice",
			description: "Cross-boundary reconcile: ask the payment processor what it ACTUALLY billed for an invoice and assert it equals the ledger to the exact minor unit. Requires a configured processor.",
			inputSchema: obj(map[string]any{"invoice_id": str("invoice id")}, "invoice_id"),
			handler:     s.verifyInvoiceTool,
		},
		{
			name:        "collect_invoice",
			description: "Attempt to collect payment for an unpaid invoice now (a dunning retry). Routes by decline reason and updates the recovery state. Requires a configured processor.",
			inputSchema: obj(map[string]any{"invoice_id": str("invoice id")}, "invoice_id"),
			handler:     s.collectInvoiceTool,
		},
		{
			name:        "run_dunning",
			description: "Process every collection whose retry is due (the dunning tick). Requires a configured processor. Returns how many were processed and their outcomes.",
			inputSchema: obj(map[string]any{}),
			handler:     s.runDunningTool,
		},
		{
			name:        "get_dunning_templates",
			description: "List the customer-facing dunning copy for each event (payment_failed, requires_action, recovered, uncollectible) — the operator's override or the built-in default.",
			inputSchema: obj(map[string]any{}),
			handler:     s.getDunningTemplatesTool,
		},
		{
			name:        "preview_dunning_message",
			description: "Render a dunning message for an event with sample data — the live preview of exactly what a customer would receive.",
			inputSchema: obj(map[string]any{"event": str("payment_failed|requires_action|recovered|uncollectible")}, "event"),
			handler:     s.previewDunningMessageTool,
		},
		{
			name:        "set_dunning_template",
			description: "Override the customer-facing copy for one dunning event. Validated to render before saving. Body fields: {{.CustomerName}}, {{.Amount}}, {{.Currency}}, {{.NextAttempt}}, {{.Reason}}, {{.UpdateURL}}.",
			inputSchema: obj(map[string]any{
				"event":   str("payment_failed|requires_action|recovered|uncollectible"),
				"subject": str("message subject"),
				"body":    str("message body, with template fields"),
			}, "event", "subject", "body"),
			handler: s.setDunningTemplateTool,
		},
		{
			name:        "get_started",
			description: "Orient yourself on a new connection: returns the account state and the recommended next step. On an empty account it explains the one-step setup. Call this first.",
			inputSchema: obj(map[string]any{}),
			handler:     s.getStartedTool,
		},
		{
			name:        "quickstart_billing",
			description: "Set up working usage billing in ONE step from a plain description — the fastest path from zero to a real bill. Creates a meter, a plan (optional flat base + a per-unit price), and a demo customer you can preview immediately. Everything has sensible defaults; at minimum just call it with no arguments. Prefer this over wiring create_meter/create_plan/attach_plan by hand when a user says 'set up billing'.",
			inputSchema: obj(map[string]any{
				"product_name":       str("your product's name; default 'My App'"),
				"meter_code":         str("what you meter, e.g. 'tokens' or 'api_calls'; default 'usage'"),
				"property_key":       str("the event property to sum, e.g. 'n'; default 'units'"),
				"unit_price":         str("price per unit as a decimal string; default '0.001'"),
				"base_fee":           str("optional flat monthly base as a decimal string; default '0' (none)"),
				"currency":           str("ISO-4217; default 'USD'"),
				"with_demo_customer": map[string]any{"type": "boolean", "description": "also create a demo customer + subscription to preview immediately; default true"},
			}),
			handler: s.quickstartBillingTool,
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

func (s *Server) scanDriftTool(_ context.Context, _ json.RawMessage) (string, error) {
	invs, err := s.store.ListInvoices()
	if err != nil {
		return "", err
	}
	var scanned, consistent int
	atRisk := map[string]decimal.Decimal{}
	var lines []string
	for _, inv := range invs {
		ledger, lerr := s.store.GetLedger(inv.ID)
		if lerr != nil || len(ledger) == 0 {
			continue
		}
		sub, ok, serr := s.store.GetSubscription(inv.SubscriptionID)
		if serr != nil || !ok {
			continue
		}
		sub.CurrentPeriodStart = inv.PeriodStart
		sub.CurrentPeriodEnd = inv.PeriodEnd
		live, cerr := engine.Compute(s.store, sub)
		if cerr != nil {
			continue
		}
		scanned++
		if reconcile.Build(inv, ledger, live).Consistent {
			consistent++
			continue
		}
		d := inv.Total.Sub(live.Invoice.Total).Abs()
		atRisk[inv.Currency] = atRisk[inv.Currency].Add(d)
		lines = append(lines, fmt.Sprintf("  • %s (customer %s): stored %s vs live %s, drift %s %s",
			inv.ID, inv.CustomerID, money.Format(inv.Total, inv.Currency), money.Format(live.Invoice.Total, inv.Currency),
			money.Format(d, inv.Currency), inv.Currency))
	}
	if scanned == 0 {
		return "No finalized invoices to scan yet.", nil
	}
	if len(lines) == 0 {
		return fmt.Sprintf("Scanned %d finalized invoice(s): ALL consistent. Every bill provably matches its events — no drift, no leakage.", scanned), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DRIFT DETECTED across the account: %d of %d invoice(s) disagree with the live events.\n", len(lines), scanned)
	b.WriteString("Money at risk:")
	for cur, amt := range atRisk {
		fmt.Fprintf(&b, " %s %s", money.Format(amt, cur), cur)
	}
	b.WriteString("\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("Reconcile each with reconcile_invoice to see the exact line-level cause.")
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

func (s *Server) revenueRecognitionTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		InvoiceID string `json:"invoice_id"`
		AsOf      string `json:"as_of"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.InvoiceID == "" {
		return "", fmt.Errorf("invoice_id is required")
	}
	inv, ok, err := s.store.GetInvoice(a.InvoiceID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown invoice_id %q", a.InvoiceID)
	}
	asOf := s.now()
	if a.AsOf != "" {
		t, perr := time.Parse(time.RFC3339, a.AsOf)
		if perr != nil {
			return "", fmt.Errorf("as_of must be RFC3339")
		}
		asOf = t
	}
	r := revrec.Recognize(inv, asOf)
	return fmt.Sprintf("Invoice %s revenue recognition (straight-line, as of %s): recognized %s %s and deferred %s %s of %s total. Service period %s → %s, fraction recognized %s.",
		r.InvoiceID, asOf.Format(time.RFC3339), r.TotalRecognized, r.Currency, r.TotalDeferred, r.Currency, r.TotalAmount,
		r.PeriodStart.Format("2006-01-02"), r.PeriodEnd.Format("2006-01-02"), r.Fraction), nil
}

func (s *Server) recordUsageTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		IdempotencyKey string         `json:"idempotency_key"`
		CustomerID     string         `json:"customer_id"`
		MeterCode      string         `json:"meter_code"`
		Properties     map[string]any `json:"properties"`
		EventTime      string         `json:"event_time"`
	}
	// UseNumber so a quantity in properties stays exact (json.Number) instead of
	// becoming a float64 — same float-free guarantee as the REST ingest path.
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.IdempotencyKey == "" || a.CustomerID == "" || a.MeterCode == "" {
		return "", fmt.Errorf("idempotency_key, customer_id and meter_code are required")
	}
	et := s.now()
	if a.EventTime != "" {
		t, err := time.Parse(time.RFC3339, a.EventTime)
		if err != nil {
			return "", fmt.Errorf("event_time must be RFC3339")
		}
		et = t
	}
	if _, err := s.ing.Accept(domain.Event{
		IdempotencyKey: a.IdempotencyKey, CustomerID: a.CustomerID, MeterCode: a.MeterCode,
		EventTime: et, Properties: a.Properties,
	}, s.now()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Recorded usage event %q for %s on meter %s (idempotent — a repeat is never double-counted).",
		a.IdempotencyKey, a.CustomerID, a.MeterCode), nil
}

func (s *Server) createEntitlementTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID, Feature, Kind, MeterCode, LimitValue, PeriodStart, PeriodEnd string
	}
	if err := json.Unmarshal(args, &struct {
		CustomerID  *string `json:"customer_id"`
		Feature     *string `json:"feature"`
		Kind        *string `json:"kind"`
		MeterCode   *string `json:"meter_code"`
		LimitValue  *string `json:"limit_value"`
		PeriodStart *string `json:"period_start"`
		PeriodEnd   *string `json:"period_end"`
	}{&a.CustomerID, &a.Feature, &a.Kind, &a.MeterCode, &a.LimitValue, &a.PeriodStart, &a.PeriodEnd}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if _, ok, _ := s.store.GetCustomer(a.CustomerID); !ok {
		return "", fmt.Errorf("unknown customer_id %q", a.CustomerID)
	}
	kind := domain.EntitlementKind(a.Kind)
	if kind != domain.EntBoolean && kind != domain.EntMetered {
		return "", fmt.Errorf("kind must be boolean|metered")
	}
	e := domain.Entitlement{ID: id.New("ent"), CustomerID: a.CustomerID, Feature: a.Feature, Kind: kind, MeterCode: a.MeterCode}
	if kind == domain.EntMetered {
		lv, err := decimal.NewFromString(a.LimitValue)
		if err != nil {
			return "", fmt.Errorf("limit_value must be a decimal string for a metered entitlement")
		}
		e.LimitValue = lv
		start, end, err := s.resolvePeriod(a.PeriodStart, a.PeriodEnd)
		if err != nil {
			return "", err
		}
		e.PeriodStart, e.PeriodEnd = start, end
	}
	if err := s.store.PutEntitlement(e); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created %s entitlement %q for customer %s.", a.Kind, a.Feature, a.CustomerID), nil
}

func (s *Server) checkEntitlementTool(_ context.Context, args json.RawMessage) (string, error) {
	cid, err := singleCustomerID(args)
	if err != nil {
		return "", err
	}
	sts, err := engine.CheckEntitlements(s.store, cid)
	if err != nil {
		return "", err
	}
	if len(sts) == 0 {
		return fmt.Sprintf("Customer %s has no entitlements.", cid), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Entitlements for %s:\n", cid)
	for _, e := range sts {
		if e.Kind == "metered" {
			verdict := "within limit"
			if !e.WithinLimit {
				verdict = "OVER LIMIT"
			}
			fmt.Fprintf(&b, "  • %s: %s/%s used (%s%%) — %s\n", e.Feature, e.Used, e.Limit, e.PctUsed, verdict)
		} else {
			fmt.Fprintf(&b, "  • %s: enabled\n", e.Feature)
		}
	}
	return b.String(), nil
}

func (s *Server) listMetersTool(_ context.Context, _ json.RawMessage) (string, error) {
	ms, err := s.store.Meters()
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "No meters defined. Create one with create_meter.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d meter(s):\n", len(ms))
	for _, m := range ms {
		fmt.Fprintf(&b, "  • %s (%s)\n", m.Code, m.Aggregation)
	}
	return b.String(), nil
}

func (s *Server) listPlansTool(_ context.Context, _ json.RawMessage) (string, error) {
	ps, err := s.store.ListPlans()
	if err != nil {
		return "", err
	}
	if len(ps) == 0 {
		return "No plans yet. Create one with create_plan.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d plan(s):\n", len(ps))
	for _, p := range ps {
		parts := make([]string, 0, len(p.Prices))
		for _, pr := range p.Prices {
			code := pr.MeterCode
			if code == "" {
				code = "base"
			}
			parts = append(parts, fmt.Sprintf("%s:%s", code, pr.Model))
		}
		fmt.Fprintf(&b, "  • %s — %s v%d [%s]\n", p.ID, p.Name, p.Version, strings.Join(parts, ", "))
	}
	return b.String(), nil
}

func (s *Server) listSubscriptionsTool(_ context.Context, _ json.RawMessage) (string, error) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		return "", err
	}
	if len(subs) == 0 {
		return "No subscriptions yet. Attach a plan to a customer with attach_plan.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d subscription(s):\n", len(subs))
	for _, sub := range subs {
		fmt.Fprintf(&b, "  • %s — customer %s on plan %s (%s), period %s → %s\n",
			sub.ID, sub.CustomerID, sub.PlanID, sub.Status,
			sub.CurrentPeriodStart.Format("2006-01-02"), sub.CurrentPeriodEnd.Format("2006-01-02"))
	}
	return b.String(), nil
}

func (s *Server) listInvoicesTool(_ context.Context, _ json.RawMessage) (string, error) {
	invs, err := s.store.ListInvoices()
	if err != nil {
		return "", err
	}
	if len(invs) == 0 {
		return "No invoices yet. Finalize a subscription's period with finalize_invoice.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d invoice(s):\n", len(invs))
	for _, inv := range invs {
		fmt.Fprintf(&b, "  • %s — customer %s, %s → %s, %s %s [%s]\n",
			inv.ID, inv.CustomerID,
			inv.PeriodStart.Format("2006-01-02"), inv.PeriodEnd.Format("2006-01-02"),
			money.Format(inv.Total, inv.Currency), inv.Currency, inv.Status)
	}
	return b.String(), nil
}

func (s *Server) listWebhooksTool(_ context.Context, _ json.RawMessage) (string, error) {
	whs, err := s.store.Webhooks()
	if err != nil {
		return "", err
	}
	if len(whs) == 0 {
		return "No webhooks registered.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d webhook(s):\n", len(whs))
	for _, w := range whs {
		ev := "all events"
		if len(w.Events) > 0 {
			ev = strings.Join(w.Events, ", ")
		}
		fmt.Fprintf(&b, "  • %s → %s [%s]\n", w.ID, w.URL, ev)
	}
	return b.String(), nil
}

func (s *Server) topupWalletTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID, Amount, Currency, Reason, IdempotencyKey string
	}
	if err := json.Unmarshal(args, &struct {
		CustomerID     *string `json:"customer_id"`
		Amount         *string `json:"amount"`
		Currency       *string `json:"currency"`
		Reason         *string `json:"reason"`
		IdempotencyKey *string `json:"idempotency_key"`
	}{&a.CustomerID, &a.Amount, &a.Currency, &a.Reason, &a.IdempotencyKey}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if _, ok, _ := s.store.GetCustomer(a.CustomerID); !ok {
		return "", fmt.Errorf("unknown customer_id %q", a.CustomerID)
	}
	amt, err := decimal.NewFromString(a.Amount)
	if err != nil || !amt.IsPositive() {
		return "", fmt.Errorf("amount must be a positive decimal string")
	}
	currency := a.Currency
	if currency == "" {
		currency = "USD"
	}
	// Crediting a wallet is a real financial state change — gate it behind arming.
	if !s.armed {
		return s.preview(fmt.Sprintf("Would credit %s %s to customer %s.", money.Format(amt, currency), currency, a.CustomerID)), nil
	}
	if s.overCap(amt) {
		return s.capMsg(amt, currency), nil
	}
	w, err := s.store.TopUpWallet(a.CustomerID, amt, currency, a.Reason, a.IdempotencyKey)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Credited %s %s to %s. New balance: %s %s.",
		money.Format(amt, currency), currency, a.CustomerID, money.Format(w.Balance, w.Currency), w.Currency), nil
}

func (s *Server) getWalletTool(_ context.Context, args json.RawMessage) (string, error) {
	cid, err := singleCustomerID(args)
	if err != nil {
		return "", err
	}
	w, ok, err := s.store.Wallet(cid)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("Customer %s has no wallet yet (balance 0).", cid), nil
	}
	return fmt.Sprintf("Wallet for %s: balance %s %s.", cid, money.Format(w.Balance, w.Currency), w.Currency), nil
}

func (s *Server) verifyInvoiceTool(ctx context.Context, args json.RawMessage) (string, error) {
	if s.proc == nil {
		return "", fmt.Errorf("no payment processor configured; verify needs a rail")
	}
	var invID string
	if err := json.Unmarshal(args, &struct {
		InvoiceID *string `json:"invoice_id"`
	}{&invID}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	inv, ok, err := s.store.GetInvoice(invID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown invoice_id %q", invID)
	}
	if inv.StripeInvoiceID == "" {
		return "", fmt.Errorf("invoice %q has not been pushed to a processor", invID)
	}
	fetched, err := s.proc.FetchInvoice(ctx, inv.StripeInvoiceID)
	if err != nil {
		return "", err
	}
	ledgerMinor := money.New(inv.Total, inv.Currency).MinorUnits()
	if fetched.AmountMinor == ledgerMinor {
		return fmt.Sprintf("Invoice %s verifies: the %s processor billed exactly the ledger amount (%d %s minor units).",
			inv.ID, s.proc.Name(), ledgerMinor, inv.Currency), nil
	}
	return fmt.Sprintf("DRIFT across the processor on %s: ledger %d vs processor %d minor units (drift %d). The processor billed something different — a tax line, a manual edit, or a rounding rule the internal ledger can't see.",
		inv.ID, ledgerMinor, fetched.AmountMinor, fetched.AmountMinor-ledgerMinor), nil
}

func (s *Server) collectInvoiceTool(ctx context.Context, args json.RawMessage) (string, error) {
	if s.proc == nil {
		return "", fmt.Errorf("no payment processor configured; collection needs a rail")
	}
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
		return "", fmt.Errorf("no collection for invoice %q", invID)
	}
	if dunning.Status(col.Status).Terminal() {
		return fmt.Sprintf("Collection for %s is already %s — nothing to do.", invID, col.Status), nil
	}
	// Collection asks the processor to charge a card — gate it behind arming.
	if inv, ok, _ := s.store.GetInvoice(invID); ok {
		if !s.armed {
			return s.preview(fmt.Sprintf("Would attempt to collect invoice %s (total %s %s) via the %s rail.",
				invID, money.Format(inv.Total, inv.Currency), inv.Currency, s.proc.Name())), nil
		}
		if s.overCap(inv.Total) {
			return s.capMsg(inv.Total, inv.Currency), nil
		}
	} else if !s.armed {
		return s.preview(fmt.Sprintf("Would attempt to collect invoice %s via the %s rail.", invID, s.proc.Name())), nil
	}
	col, err = s.attemptCollectionMCP(ctx, col)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Collection for %s: status %s after %d attempt(s)", col.InvoiceID, col.Status, col.Attempts)
	if col.LastReason != "" {
		msg += fmt.Sprintf(", last decline: %s", col.LastReason)
	}
	return msg + ".", nil
}

func (s *Server) runDunningTool(ctx context.Context, _ json.RawMessage) (string, error) {
	if s.proc == nil {
		return "", fmt.Errorf("no payment processor configured; dunning needs a rail")
	}
	due, err := s.store.CollectionsDue(s.now())
	if err != nil {
		return "", err
	}
	if len(due) == 0 {
		return "No collections are due. Nothing to run.", nil
	}
	// run_dunning attempts real charges across every due collection — gate it.
	if !s.armed {
		return s.preview(fmt.Sprintf("Would attempt charges on %d due collection(s) via the %s rail.", len(due), s.proc.Name())), nil
	}
	counts := map[string]int{}
	var faults int
	for _, col := range due {
		updated, err := s.attemptCollectionMCP(ctx, col)
		if err != nil {
			faults++
			continue
		}
		counts[updated.Status]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ran dunning on %d due collection(s):", len(due))
	for st, n := range counts {
		fmt.Fprintf(&b, " %d %s,", n, st)
	}
	if faults > 0 {
		fmt.Fprintf(&b, " %d processor fault(s)", faults)
	}
	return strings.TrimRight(b.String(), ","), nil
}

// attemptCollectionMCP runs one charge attempt and folds the outcome through the
// pure dunning state machine (the same core the HTTP path uses). Webhooks are NOT
// emitted here — those fire from the HTTP/cron dunning path; this is the operator
// driving a retry interactively and reading the result.
func (s *Server) attemptCollectionMCP(ctx context.Context, col domain.Collection) (domain.Collection, error) {
	res, err := s.proc.ChargeInvoice(ctx, col.ExternalID)
	if err != nil {
		return col, err
	}
	now := s.now()
	st := dunning.State{Status: dunning.Status(col.Status), Attempts: col.Attempts, LastReason: col.LastReason}
	if col.FirstFailedAt != nil {
		st.FirstFailedAt = *col.FirstFailedAt
	}
	if col.NextAttemptAt != nil {
		st.NextAttemptAt = *col.NextAttemptAt
	}
	st = st.RecordAttempt(res.Status == "paid", res.FailureReason, dunning.DefaultSchedule, now)
	col.Status = string(st.Status)
	col.Attempts = st.Attempts
	col.LastReason = st.LastReason
	col.FirstFailedAt = nilTime(st.FirstFailedAt)
	col.NextAttemptAt = nilTime(st.NextAttemptAt)
	col.UpdatedAt = now
	if err := s.store.PutCollection(col); err != nil {
		return col, err
	}
	return col, nil
}

func nilTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Server) loadTemplatesMCP() map[comms.Event]comms.Template {
	out := map[comms.Event]comms.Template{}
	tmpls, _ := s.store.MessageTemplates()
	for _, t := range tmpls {
		out[comms.Event(t.Event)] = comms.Template{Subject: t.Subject, Body: t.Body}
	}
	return out
}

func sampleCommsCtx() comms.Context {
	return comms.Context{
		CustomerName: "Acme Inc", InvoiceID: "inv_demo", Amount: "64.00", Currency: "USD",
		Attempts: 2, NextAttempt: "2026-06-25", Reason: "insufficient_funds", UpdateURL: "https://your-app.com/billing",
	}
}

func validCommsEvent(e string) bool {
	for _, a := range comms.AllEvents {
		if string(a) == e {
			return true
		}
	}
	return false
}

func (s *Server) getDunningTemplatesTool(_ context.Context, _ json.RawMessage) (string, error) {
	ov := s.loadTemplatesMCP()
	var b strings.Builder
	b.WriteString("Dunning message templates:\n")
	for _, e := range comms.AllEvents {
		t := comms.For(e, ov)
		tag := "default"
		if _, custom := ov[e]; custom {
			tag = "customized"
		}
		fmt.Fprintf(&b, "  • %s (%s): %q\n", e, tag, t.Subject)
	}
	return b.String(), nil
}

func (s *Server) previewDunningMessageTool(_ context.Context, args json.RawMessage) (string, error) {
	var ev string
	if err := json.Unmarshal(args, &struct {
		Event *string `json:"event"`
	}{&ev}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if !validCommsEvent(ev) {
		return "", fmt.Errorf("unknown event; one of payment_failed|requires_action|recovered|uncollectible")
	}
	msg, err := comms.Render(comms.For(comms.Event(ev), s.loadTemplatesMCP()), sampleCommsCtx())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Subject: %s\n\n%s", msg.Subject, msg.Body), nil
}

func (s *Server) setDunningTemplateTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct{ Event, Subject, Body string }
	if err := json.Unmarshal(args, &struct {
		Event   *string `json:"event"`
		Subject *string `json:"subject"`
		Body    *string `json:"body"`
	}{&a.Event, &a.Subject, &a.Body}); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if !validCommsEvent(a.Event) {
		return "", fmt.Errorf("unknown event; one of payment_failed|requires_action|recovered|uncollectible")
	}
	if a.Subject == "" || a.Body == "" {
		return "", fmt.Errorf("subject and body are required")
	}
	if _, err := comms.Render(comms.Template{Subject: a.Subject, Body: a.Body}, sampleCommsCtx()); err != nil {
		return "", fmt.Errorf("template does not render: %w", err)
	}
	if err := s.store.PutMessageTemplate(domain.MessageTemplate{Event: a.Event, Subject: a.Subject, Body: a.Body, UpdatedAt: s.now()}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated the %q dunning message.", a.Event), nil
}

func (s *Server) getStartedTool(_ context.Context, _ json.RawMessage) (string, error) {
	customers, err := s.store.ListCustomers()
	if err != nil {
		return "", err
	}
	meters, err := s.store.Meters()
	if err != nil {
		return "", err
	}
	if len(customers) == 0 && len(meters) == 0 {
		return "Welcome to smolbill. Your account is empty.\n" +
			"Fastest path: tell me about your product and I'll set it up in one step with quickstart_billing " +
			"(e.g. \"meter tokens at $0.001 each with a $20 base\").\n" +
			"Or build it piece by piece: create_customer → create_meter → create_plan → attach_plan.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Account state: %d customer(s), %d meter(s) defined.\n", len(customers), len(meters))
	b.WriteString("Next: record_usage to meter activity, get_usage / preview_invoice to see a bill, " +
		"finalize_invoice to materialize it, get_analytics for the account view.")
	return b.String(), nil
}

// quickstartBillingTool is the "extremely easy" path: one call sets up a working
// billing configuration from a plain description, with sensible defaults for
// everything, so an agent can go from "set up billing for my app" to a real
// previewable invoice without wiring four tools by hand.
func (s *Server) quickstartBillingTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ProductName      string `json:"product_name"`
		MeterCode        string `json:"meter_code"`
		PropertyKey      string `json:"property_key"`
		UnitPrice        string `json:"unit_price"`
		BaseFee          string `json:"base_fee"`
		Currency         string `json:"currency"`
		WithDemoCustomer *bool  `json:"with_demo_customer"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if a.ProductName == "" {
		a.ProductName = "My App"
	}
	if a.MeterCode == "" {
		a.MeterCode = "usage"
	}
	if a.PropertyKey == "" {
		a.PropertyKey = "units"
	}
	if a.UnitPrice == "" {
		a.UnitPrice = "0.001"
	}
	if a.Currency == "" {
		a.Currency = "USD"
	}
	withDemo := true
	if a.WithDemoCustomer != nil {
		withDemo = *a.WithDemoCustomer
	}
	if _, err := decimal.NewFromString(a.UnitPrice); err != nil {
		return "", fmt.Errorf("unit_price must be a decimal string")
	}
	hasBase := a.BaseFee != "" && a.BaseFee != "0"
	if hasBase {
		if _, err := decimal.NewFromString(a.BaseFee); err != nil {
			return "", fmt.Errorf("base_fee must be a decimal string")
		}
	}

	m := domain.Meter{
		ID: id.New("mtr"), Code: a.MeterCode, Name: a.ProductName + " " + a.MeterCode,
		Aggregation: domain.AggSum, PropertyKey: a.PropertyKey, CreatedAt: s.now(),
	}
	if err := s.store.PutMeter(m); err != nil {
		return "", err
	}

	prices := make([]engine.PriceInput, 0, 2)
	if hasBase {
		prices = append(prices, engine.PriceInput{Model: "flat", Currency: a.Currency, FlatAmount: a.BaseFee})
	}
	prices = append(prices, engine.PriceInput{Model: "per_unit", Currency: a.Currency, MeterCode: a.MeterCode, UnitAmount: a.UnitPrice})
	plan, err := engine.BuildPlan(engine.PlanInput{Name: a.ProductName + " plan", Prices: prices})
	if err != nil {
		return "", err
	}
	plan.CreatedAt = s.now()
	if err := s.store.PutPlan(plan); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Billing is set up for %s:\n", a.ProductName)
	fmt.Fprintf(&b, "  • meter %q (sum of '%s')\n", a.MeterCode, a.PropertyKey)
	if hasBase {
		fmt.Fprintf(&b, "  • plan %s: %s %s base + %s %s per %s\n", plan.ID, a.BaseFee, a.Currency, a.UnitPrice, a.Currency, a.MeterCode)
	} else {
		fmt.Fprintf(&b, "  • plan %s: %s %s per %s\n", plan.ID, a.UnitPrice, a.Currency, a.MeterCode)
	}

	if withDemo {
		cust := domain.Customer{ID: id.New("cus"), Name: "Demo (" + a.ProductName + ")", CreatedAt: s.now()}
		if err := s.store.PutCustomer(cust); err != nil {
			return "", err
		}
		start, end, _ := s.resolvePeriod("", "")
		sub := domain.Subscription{
			ID: id.New("sub"), CustomerID: cust.ID, PlanID: plan.ID, PlanVersion: plan.Version,
			Status: domain.SubActive, CurrentPeriodStart: start, CurrentPeriodEnd: end, StartedAt: start,
		}
		if err := s.store.PutSubscription(sub); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  • demo customer %s on the plan (subscription %s)\n", cust.ID, sub.ID)
		fmt.Fprintf(&b, "\nTry it now: record_usage for %s on meter %q (e.g. {\"%s\": 1000}), then preview_invoice to see the bill.",
			cust.ID, a.MeterCode, a.PropertyKey)
	} else {
		b.WriteString("\nNext: create_customer, then attach_plan to put them on this plan.")
	}
	return b.String(), nil
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
	var a struct {
		CustomerID string `json:"customer_id"`
		AsOf       string `json:"as_of"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.CustomerID == "" {
		return "", fmt.Errorf("customer_id is required")
	}
	cid := a.CustomerID

	var (
		res invoice.Result
		sub domain.Subscription
		err error
	)
	if a.AsOf != "" {
		asOf, perr := time.Parse(time.RFC3339, a.AsOf)
		if perr != nil {
			return "", fmt.Errorf("as_of must be RFC3339")
		}
		res, sub, err = engine.ComputeAsOfForActiveSub(s.store, cid, asOf)
	} else {
		res, sub, err = engine.ComputeForActiveSub(s.store, cid)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	when := "current"
	if a.AsOf != "" {
		when = "as of " + a.AsOf
	}
	fmt.Fprintf(&b, "Usage for %s (%s, period %s → %s):\n", cid, when,
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
