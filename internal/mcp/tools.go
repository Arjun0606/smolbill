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
	}
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
