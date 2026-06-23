// Package dodo is a thin, transparent Dodo Payments adapter implementing
// payments.Processor. Dodo is a Merchant of Record (MoR): it collects from the
// end customer, remits tax, and runs its own card retries. smolbill therefore
// uses Dodo to *collect a finalized total* and to *read back what it billed* for
// cross-rail reconciliation — it never holds funds itself (build plan §2, §16).
//
// This is also the rail we bill our OWN Cloud Pro on (BUSINESS.md): smolbill
// dogfoods smolbill, and Dodo is the processor underneath.
//
// Unlike the Stripe adapter (which itemizes via invoice items), Dodo collects a
// single payment for the invoice total. That is the correct mapping for a
// product/MoR processor: the deterministic engine owns the line breakdown and the
// reconciliation proof; the processor only needs the authoritative total to
// collect, and FetchInvoice reconciles on that total in exact minor units.
//
// No SDK: a small net/http client over Dodo's REST API keeps the single-binary
// promise and makes every field on the wire auditable. Amounts are always sent as
// exact integer minor units (cents) — never a float.
//
// NOTE: Dodo's product model means an arbitrary-amount collection maps to a
// one-time payment carrying the computed total. Field names follow Dodo's
// documented Payments API; verify against a live test key before production use.
package dodo

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

const (
	liveBaseURL = "https://live.dodopayments.com"
	testBaseURL = "https://test.dodopayments.com"
)

// Client is a minimal Dodo Payments API client.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (a mock in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Dodo client. apiKey is the secret API key. live selects the live
// API root; false uses the test root.
func New(apiKey string, live bool, opts ...Option) *Client {
	base := testBaseURL
	if live {
		base = liveBaseURL
	}
	c := &Client{apiKey: apiKey, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Dodo processor from DODO_PAYMENTS_API_KEY. DODO_PAYMENTS_LIVE
// ("1"/"true") selects the live API root (default: test). Returns ok=false with no
// error when the key is unset, so the registry can skip to the next rail.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("DODO_PAYMENTS_API_KEY")
	if key == "" {
		return nil, false, nil
	}
	live := isTrue(os.Getenv("DODO_PAYMENTS_LIVE"))
	return New(key, live), true, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "live":
		return true
	}
	return false
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "dodo" }

// PushInvoice creates (idempotently) a customer, then a one-time payment for the
// invoice total. The reconciliation hash is stamped in metadata so the Dodo record
// links back to smolbill's proof. Returns the Dodo payment id, status, and hosted
// payment link.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	// 1. Customer (reused across pushes via a stable idempotency key).
	custBody := map[string]any{
		"name":     req.Customer.Name,
		"metadata": map[string]string{"smolbill_customer_id": req.Customer.ID},
	}
	if req.Customer.ExternalID != "" {
		custBody["metadata"].(map[string]string)["external_id"] = req.Customer.ExternalID
	}
	var cust struct {
		CustomerID string `json:"customer_id"`
	}
	if err := c.post(ctx, "/customers", custBody, "cust:"+req.Customer.ID, &cust); err != nil {
		return payments.PushResult{}, fmt.Errorf("dodo: create customer: %w", err)
	}

	// 2. One-time payment for the authoritative total, in integer minor units.
	total := money.New(inv.Total, inv.Currency).RoundDown()
	payBody := map[string]any{
		"payment_link": true,
		"customer":     map[string]string{"customer_id": cust.CustomerID},
		"amount":       total.MinorUnits(),
		"currency":     strings.ToUpper(inv.Currency),
		"metadata": map[string]string{
			"smolbill_invoice_id":  inv.ID,
			"reconciliation_hash":  req.Hash,
			"smolbill_customer_id": req.Customer.ID,
		},
	}
	var created struct {
		PaymentID   string `json:"payment_id"`
		Status      string `json:"status"`
		PaymentLink string `json:"payment_link"`
	}
	if err := c.post(ctx, "/payments", payBody, base+":payment", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("dodo: create payment: %w", err)
	}
	status := created.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: created.PaymentID,
		Status:     status,
		HostedURL:  created.PaymentLink,
	}, nil
}

// FetchInvoice reads a payment back from Dodo to reconcile across the money rail.
// Dodo reports amounts in integer minor units, so the comparison against
// smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		TotalAmount int64  `json:"total_amount"`
		Amount      int64  `json:"amount"`
		Currency    string `json:"currency"`
		Status      string `json:"status"`
	}
	if err := c.get(ctx, "/payments/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	amount := resp.TotalAmount
	if amount == 0 {
		amount = resp.Amount
	}
	return payments.FetchedInvoice{
		AmountMinor: amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported for a Merchant-of-Record rail. Dodo
// collects from the customer via the hosted payment link and runs its OWN card
// retries (dunning), so there is no off-session pull for smolbill to drive. We
// return a transport-style error (not a "failed" ChargeResult) so smolbill's
// internal dunning surfaces this rather than mis-routing it as a card decline.
// For Dodo, leave smolbill dunning disabled and let the MoR recover.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("dodo: collection is managed by the merchant-of-record; disable smolbill dunning for this processor")
}

// post sends a JSON request to Dodo with bearer auth and an idempotency key,
// decoding a 2xx JSON body into out (if non-nil) and turning a non-2xx into a
// structured error.
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

// get issues an authenticated GET to Dodo and decodes a 2xx JSON body.
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
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case e.Error.Message != "":
			return fmt.Errorf("dodo %d: %s (%s)", resp.StatusCode, e.Error.Message, e.Error.Code)
		case e.Message != "":
			return fmt.Errorf("dodo %d: %s", resp.StatusCode, e.Message)
		default:
			return fmt.Errorf("dodo %d: %s", resp.StatusCode, string(body))
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("dodo: decode response: %w", err)
		}
	}
	return nil
}
