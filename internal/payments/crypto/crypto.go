// Package crypto is a thin, non-custodial stablecoin rail implementing
// payments.Processor. It targets a configurable crypto-payment endpoint (a hosted
// crypto PSP or a self-hosted settlement service — e.g. a USDC checkout on the
// same Hyperliquid stack Cabbge uses) over plain HTTP/JSON. smolbill never holds
// keys or funds: it asks the endpoint to create a payment request for the invoice
// total and later reads back the settled on-chain amount to reconcile.
//
// The defining difference from card rails: collection is PUSH-only. Funds move
// when the payer sends them on-chain; there is no off-session "pull" smolbill can
// trigger. ChargeInvoice therefore returns an error (not a "failed" decline) so
// the dunning engine surfaces it instead of pretending a card was declined.
//
// Like the other adapters, amounts cross the wire as exact integer minor units —
// never a float — and the settlement endpoint is responsible for mapping the
// fiat-denominated total to its stablecoin equivalent.
//
// NOTE: there is no single canonical crypto-payments API, so the endpoint is
// fully configurable (CRYPTO_PAY_BASE_URL). Field names follow a conventional
// {amount, currency, metadata} charge shape; verify against your chosen service.
package crypto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
)

// Client is a minimal client for a configurable crypto-payment endpoint.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a crypto rail client. apiKey authenticates to the settlement
// endpoint; baseURL is its API root (no default — it is deployment-specific).
func New(apiKey, baseURL string, opts ...Option) *Client {
	c := &Client{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a crypto processor from CRYPTO_PAY_API_KEY + CRYPTO_PAY_BASE_URL.
// Returns ok=false (no error) when the key is unset so the registry can skip it; a
// set key with no base URL is a configuration error.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("CRYPTO_PAY_API_KEY")
	if key == "" {
		return nil, false, nil
	}
	base := os.Getenv("CRYPTO_PAY_BASE_URL")
	if base == "" {
		return nil, false, fmt.Errorf("crypto: CRYPTO_PAY_API_KEY is set but CRYPTO_PAY_BASE_URL is empty")
	}
	return New(key, base), true, nil
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "crypto" }

// PushInvoice creates a payment request for the invoice total at the settlement
// endpoint, stamping the reconciliation hash so the on-chain record links back to
// smolbill's proof. Returns the charge id, status, and a hosted pay URL.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}
	total := money.New(inv.Total, inv.Currency).RoundDown()
	body := map[string]any{
		"amount":   total.MinorUnits(), // exact integer minor units, never a float
		"currency": strings.ToUpper(inv.Currency),
		"metadata": map[string]string{
			"smolbill_invoice_id":  inv.ID,
			"reconciliation_hash":  req.Hash,
			"smolbill_customer_id": req.Customer.ID,
		},
	}
	var created struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		PayURL    string `json:"pay_url"`
		HostedURL string `json:"hosted_url"`
	}
	if err := c.post(ctx, "/charges", body, base+":charge", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("crypto: create charge: %w", err)
	}
	status := created.Status
	if status == "" {
		status = "open"
	}
	hosted := created.PayURL
	if hosted == "" {
		hosted = created.HostedURL
	}
	return payments.PushResult{ExternalID: created.ID, Status: status, HostedURL: hosted}, nil
}

// FetchInvoice reads the settled amount back from the endpoint to reconcile across
// the money rail. The endpoint reports the amount in integer minor units, so the
// comparison against smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		AmountMinor int64  `json:"amount_minor"`
		Amount      int64  `json:"amount"`
		Currency    string `json:"currency"`
		Status      string `json:"status"`
	}
	if err := c.get(ctx, "/charges/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	amount := resp.AmountMinor
	if amount == 0 {
		amount = resp.Amount
	}
	return payments.FetchedInvoice{
		AmountMinor: amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice is unsupported: this is a non-custodial, push-only rail. Funds
// move only when the payer sends them on-chain, so there is no off-session pull to
// drive. Returning an error (not a "failed" ChargeResult) keeps smolbill dunning
// from mis-routing this as a card decline.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("crypto: non-custodial push-only rail has no off-session pull; collection happens on-chain")
}

func (c *Client) post(ctx context.Context, path string, body any, idempotencyKey string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case e.Message != "":
			return fmt.Errorf("crypto %d: %s", resp.StatusCode, e.Message)
		case e.Error != "":
			return fmt.Errorf("crypto %d: %s", resp.StatusCode, e.Error)
		default:
			return fmt.Errorf("crypto %d: %s", resp.StatusCode, string(body))
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("crypto: decode response: %w", err)
		}
	}
	return nil
}
