package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/engine"
)

// gateCheckTool is the real-time enforcement tool: a hard allow/deny for "may this
// customer do this right now?", computed live against a metered entitlement and/or
// the prepaid wallet. Unlike set_spend_cap (which only warns), this denies.
func (s *Server) gateCheckTool(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CustomerID string          `json:"customer_id"`
		Feature    string          `json:"feature"`
		Quantity   decimal.Decimal `json:"quantity"`
		Cost       decimal.Decimal `json:"cost"`
		Currency   string          `json:"currency"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.CustomerID == "" {
		return "", fmt.Errorf("customer_id is required")
	}
	d, err := engine.Gate(s.store, engine.GateRequest{
		CustomerID: a.CustomerID, Feature: a.Feature,
		Quantity: a.Quantity, Cost: a.Cost, Currency: a.Currency,
	})
	if err != nil {
		return "", err
	}
	verdict := "ALLOWED"
	if !d.Allowed {
		verdict = "DENIED"
	}
	msg := fmt.Sprintf("%s — %s.", verdict, d.Reason)
	if d.Feature != "" && d.Limit != "" {
		msg += fmt.Sprintf(" Feature %q: %s used of %s, %s remaining (requested %s).", d.Feature, d.Used, d.Limit, d.Remaining, d.Requested)
	}
	if d.Cost != "" {
		msg += fmt.Sprintf(" Wallet balance %s %s vs cost %s.", d.Balance, d.Currency, d.Cost)
	}
	return msg, nil
}
