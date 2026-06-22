package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/payments/fake"
	"github.com/Arjun0606/smolbill/internal/store/memory"
)

// session drives the MCP server by feeding newline-delimited JSON-RPC requests
// and collecting the responses.
type session struct {
	t   *testing.T
	srv *Server
}

func newSession(t *testing.T) (*session, *memory.Store) {
	t.Helper()
	st := memory.New()
	clock := func() time.Time { return time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC) }
	return &session{t: t, srv: New(st, ingest.New(st, 0), clock)}, st
}

// call sends one request and returns the decoded response (nil for notifications).
func (s *session) call(method string, id int, params any) map[string]any {
	s.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if id >= 0 {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	in := strings.NewReader(string(body) + "\n")
	var out strings.Builder
	if err := s.srv.Serve(context.Background(), in, &out); err != nil {
		s.t.Fatalf("serve: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		return nil
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		s.t.Fatalf("decode response %q: %v", out.String(), err)
	}
	return resp
}

// callTool invokes a tool and returns its text content + isError.
func (s *session) callTool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	resp := s.call("tools/call", 1, map[string]any{"name": name, "arguments": args})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("tools/call %s: no result: %v", name, resp)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func TestInitialize(t *testing.T) {
	s, _ := newSession(t)
	resp := s.call("initialize", 1, map[string]any{"protocolVersion": protocolVersion})
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if result["serverInfo"].(map[string]any)["name"] != "smolbill" {
		t.Fatal("serverInfo.name should be smolbill")
	}
}

func TestNotificationGetsNoReply(t *testing.T) {
	s, _ := newSession(t)
	if resp := s.call("notifications/initialized", -1, nil); resp != nil {
		t.Fatalf("notification should get no response, got %v", resp)
	}
}

func TestToolsListIsIntentOnly(t *testing.T) {
	s, _ := newSession(t)
	resp := s.call("tools/list", 1, nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)

	names := map[string]bool{}
	for _, ti := range tools {
		names[ti.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"create_meter", "create_plan", "attach_plan", "set_spend_cap", "get_usage", "preview_invoice"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
	// THE SAFETY INVARIANT: the agent can never touch money math.
	for _, forbidden := range []string{"charge", "calculate_bill", "calculate_and_charge", "refund"} {
		if names[forbidden] {
			t.Errorf("forbidden money-math tool exposed: %q", forbidden)
		}
	}
}

func TestFullIntentFlow(t *testing.T) {
	s, _ := newSession(t)

	// A customer must exist (created out-of-band, like via API).
	if err := s.srvStore().PutCustomer(domain.Customer{ID: "cus_1", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}

	// create_meter
	if text, isErr := s.callTool("create_meter", map[string]any{
		"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n",
	}); isErr {
		t.Fatalf("create_meter errored: %s", text)
	}

	// create_plan ($0.001/token)
	text, isErr := s.callTool("create_plan", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
		},
	})
	if isErr {
		t.Fatalf("create_plan errored: %s", text)
	}
	planID := extractID(t, text, "id ")

	// attach_plan (defaults to current month — June 2026 per the fixed clock)
	if text, isErr := s.callTool("attach_plan", map[string]any{
		"customer_id": "cus_1", "plan_id": planID,
	}); isErr {
		t.Fatalf("attach_plan errored: %s", text)
	}

	// Ingest some usage directly via the store's ingester path (events arrive via
	// the API/SDK, not MCP — MCP is config + read only).
	s.ingestTokens("cus_1", "e1", 3000)

	// get_usage reflects $3.00
	usage, isErr := s.callTool("get_usage", map[string]any{"customer_id": "cus_1"})
	if isErr || !strings.Contains(usage, "$3.00") {
		t.Fatalf("get_usage = %q (isErr=%v), want $3.00", usage, isErr)
	}

	// preview_invoice shows the total + a verification hash, no money moved
	prev, isErr := s.callTool("preview_invoice", map[string]any{"customer_id": "cus_1"})
	if isErr || !strings.Contains(prev, "Total: $3.00") || !strings.Contains(prev, "Verification hash:") {
		t.Fatalf("preview_invoice = %q", prev)
	}
	if !strings.Contains(prev, "No money moved") {
		t.Fatal("preview_invoice must state no money moved")
	}

	// set_spend_cap registers an alert
	if text, isErr := s.callTool("set_spend_cap", map[string]any{
		"customer_id": "cus_1", "limit": "100.00", "webhook_url": "https://hook.test/x",
	}); isErr {
		t.Fatalf("set_spend_cap errored: %s", text)
	}
}

// TestFullLifecycleByAI proves an agent can run the WHOLE thing over MCP with no
// out-of-band setup: onboard a customer, configure billing, finalize the invoice,
// catch drift, and read analytics. This is the "truly do everything by AI" claim.
func TestFullLifecycleByAI(t *testing.T) {
	s, _ := newSession(t)

	cust, isErr := s.callTool("create_customer", map[string]any{"name": "Acme"})
	if isErr {
		t.Fatalf("create_customer: %s", cust)
	}
	custID := extractID(t, cust, "id ")

	if list, _ := s.callTool("list_customers", nil); !strings.Contains(list, custID) {
		t.Fatalf("list_customers missing %s: %q", custID, list)
	}

	s.callTool("create_meter", map[string]any{"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n"})
	planText, _ := s.callTool("create_plan", map[string]any{
		"name": "Pro", "prices": []map[string]any{
			{"model": "per_unit", "currency": "USD", "meter_code": "tokens", "unit_amount": "0.001"},
		},
	})
	planID := extractID(t, planText, "id ")
	attachText, isErr := s.callTool("attach_plan", map[string]any{"customer_id": custID, "plan_id": planID})
	if isErr {
		t.Fatalf("attach_plan: %s", attachText)
	}
	subID := extractID(t, attachText, "subscription ")

	s.ingestTokens(custID, "e1", 3000) // usage arrives via the app path, $3.00

	finText, isErr := s.callTool("finalize_invoice", map[string]any{"subscription_id": subID})
	if isErr || !strings.Contains(finText, "$3.00") {
		t.Fatalf("finalize_invoice = %q (isErr=%v)", finText, isErr)
	}
	invID := extractID(t, finText, "invoice ")

	if rec, isErr := s.callTool("reconcile_invoice", map[string]any{"invoice_id": invID}); isErr || !strings.Contains(rec, "reconciles") {
		t.Fatalf("reconcile (clean) = %q", rec)
	}

	// A late event lands after finalize — reconcile must catch the drift.
	s.ingestTokens(custID, "late", 5000)
	if rec, _ := s.callTool("reconcile_invoice", map[string]any{"invoice_id": invID}); !strings.Contains(rec, "DRIFT") {
		t.Fatalf("reconcile should detect drift: %q", rec)
	}

	if an, isErr := s.callTool("get_analytics", nil); isErr || !strings.Contains(an, "customers: 1") {
		t.Fatalf("get_analytics = %q", an)
	}
}

// TestEntitlementsAndWalletByAI proves the agent can gate features and manage
// prepaid balance: a metered entitlement is checked live against recorded usage
// (within limit, then over), and a wallet topup reflects immediately.
func TestEntitlementsAndWalletByAI(t *testing.T) {
	s, _ := newSession(t)
	cust, _ := s.callTool("create_customer", map[string]any{"name": "Acme"})
	custID := extractID(t, cust, "id ")
	s.callTool("create_meter", map[string]any{"code": "tokens", "name": "Tokens", "aggregation": "sum", "property_key": "n"})

	if _, isErr := s.callTool("create_entitlement", map[string]any{
		"customer_id": custID, "feature": "tokens_quota", "kind": "metered",
		"meter_code": "tokens", "limit_value": "5000",
	}); isErr {
		t.Fatal("create_entitlement errored")
	}

	// 3000 used → within the 5000 limit.
	s.callTool("record_usage", map[string]any{"idempotency_key": "u1", "customer_id": custID, "meter_code": "tokens", "properties": map[string]any{"n": 3000}})
	if chk, _ := s.callTool("check_entitlement", map[string]any{"customer_id": custID}); !strings.Contains(chk, "within limit") {
		t.Fatalf("expected within limit: %q", chk)
	}
	// 3000 more → 6000 over 5000.
	s.callTool("record_usage", map[string]any{"idempotency_key": "u2", "customer_id": custID, "meter_code": "tokens", "properties": map[string]any{"n": 3000}})
	if chk, _ := s.callTool("check_entitlement", map[string]any{"customer_id": custID}); !strings.Contains(chk, "OVER LIMIT") {
		t.Fatalf("expected OVER LIMIT: %q", chk)
	}

	if _, isErr := s.callTool("topup_wallet", map[string]any{"customer_id": custID, "amount": "20.00", "currency": "USD"}); isErr {
		t.Fatal("topup_wallet errored")
	}
	if wal, _ := s.callTool("get_wallet", map[string]any{"customer_id": custID}); !strings.Contains(wal, "20.00") {
		t.Fatalf("wallet balance wrong: %q", wal)
	}
}

// TestDunningToolsByAI proves the agent can run failed-payment recovery when a
// processor is configured: a soft decline schedules a retry, the next attempt
// recovers.
func TestDunningToolsByAI(t *testing.T) {
	s, _ := newSession(t)
	proc := fake.New()
	s.srv.SetProcessor(proc)

	// Register an invoice with the fake (as a finalize+push would) and open a
	// collection against it.
	pr, err := proc.PushInvoice(context.Background(), payments.PushRequest{
		Invoice:        domain.Invoice{ID: "inv_1", Total: decimal.RequireFromString("10.00"), Currency: "USD"},
		IdempotencyKey: "inv_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.srvStore().PutCollection(domain.Collection{
		InvoiceID: "inv_1", ExternalID: pr.ExternalID, Status: "scheduled", Currency: "USD", AmountMinor: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	proc.ProgramCharges(pr.ExternalID, payments.ChargeResult{Status: "failed", FailureReason: "insufficient_funds"})

	if out, isErr := s.callTool("collect_invoice", map[string]any{"invoice_id": "inv_1"}); isErr || !strings.Contains(out, "retrying") {
		t.Fatalf("collect 1 = %q (isErr=%v), want retrying", out, isErr)
	}
	if out, _ := s.callTool("collect_invoice", map[string]any{"invoice_id": "inv_1"}); !strings.Contains(out, "paid") {
		t.Fatalf("collect 2 = %q, want paid (recovered)", out)
	}
}

// TestProcessorToolsNeedRail proves the verify/dunning tools fail clearly when no
// processor is configured, instead of pretending.
func TestProcessorToolsNeedRail(t *testing.T) {
	s, _ := newSession(t) // no processor
	if _, isErr := s.callTool("run_dunning", map[string]any{}); !isErr {
		t.Error("run_dunning without a processor must error")
	}
	if _, isErr := s.callTool("verify_invoice", map[string]any{"invoice_id": "x"}); !isErr {
		t.Error("verify_invoice without a processor must error")
	}
}

func TestToolErrorIsInResultNotRPCError(t *testing.T) {
	s, _ := newSession(t)
	text, isErr := s.callTool("get_usage", map[string]any{"customer_id": "nobody"})
	if !isErr {
		t.Fatal("expected isError=true for unknown customer")
	}
	if !strings.Contains(text, "no active subscription") {
		t.Fatalf("error text = %q", text)
	}
}

// --- helpers exposing internals to the test ---

func (s *session) srvStore() *memory.Store { return s.srv.store.(*memory.Store) }

func (s *session) ingestTokens(cid, key string, n int) {
	s.t.Helper()
	_, err := s.srv.ing.Accept(domain.Event{
		IdempotencyKey: key, CustomerID: cid, MeterCode: "tokens",
		EventTime: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), Properties: map[string]any{"n": n},
	}, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		s.t.Fatal(err)
	}
}

func extractID(t *testing.T, text, marker string) string {
	t.Helper()
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("no %q in %q", marker, text)
	}
	rest := text[i+len(marker):]
	// id runs until the next ')' or space.
	end := strings.IndexAny(rest, ") ")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}
