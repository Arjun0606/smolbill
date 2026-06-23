// Package razorpay is a thin, transparent Razorpay adapter implementing
// payments.Processor. Razorpay collects payment for a finalized invoice via its
// hosted invoice page (short_url) — smolbill never holds funds itself (build plan
// §2, §16). smolbill uses Razorpay to *collect a finalized total* and to *read
// back what it billed* for cross-rail reconciliation.
//
// Like the Dodo adapter (and unlike Stripe's itemized invoice items), this adapter
// pushes the authoritative invoice TOTAL as a single amount: the deterministic
// engine owns the line breakdown and the reconciliation proof; the processor only
// needs the total to collect, and FetchInvoice reconciles on it in exact minor
// units (paise/cents).
//
// The key difference from Dodo: Razorpay authenticates with HTTP BASIC auth
// (key_id:key_secret), not a bearer token. Every request carries SetBasicAuth.
//
// No SDK: a small net/http client over Razorpay's REST API keeps the single-binary
// promise and makes every field on the wire auditable. Amounts are always sent as
// exact integer minor units (paise/cents) — never a float.
//
// NOTE: Field names follow Razorpay's documented Invoices API; verify against a
// live test key before production use.
package razorpay

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

const defaultBaseURL = "https://api.razorpay.com/v1"

// Client is a minimal Razorpay API client.
type Client struct {
	keyID     string
	keySecret string
	baseURL   string
	http      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (a mock in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Razorpay client. keyID and keySecret are the API key pair used for
// HTTP BASIC auth on every request.
func New(keyID, keySecret string, opts ...Option) *Client {
	c := &Client{
		keyID:     keyID,
		keySecret: keySecret,
		baseURL:   defaultBaseURL,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Razorpay processor from RAZORPAY_KEY_ID and RAZORPAY_KEY_SECRET.
// Returns ok=false with no error when the key id is unset, so the registry can skip
// to the next rail.
func FromEnv() (payments.Processor, bool, error) {
	keyID := os.Getenv("RAZORPAY_KEY_ID")
	if keyID == "" {
		return nil, false, nil
	}
	return New(keyID, os.Getenv("RAZORPAY_KEY_SECRET")), true, nil
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "razorpay" }

// PushInvoice creates a Razorpay invoice for the authoritative total, in integer
// minor units (paise/cents). The reconciliation hash and smolbill ids are stamped
// in the invoice notes so the Razorpay record links back to smolbill's proof.
// Returns the Razorpay invoice id, status, and hosted short_url.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	total := money.New(inv.Total, inv.Currency).RoundDown()
	body := map[string]any{
		"type":     "invoice",
		"currency": strings.ToUpper(inv.Currency),
		"customer": map[string]string{"name": req.Customer.Name},
		// Razorpay line items take amount in integer minor units. The deterministic
		// engine owns the breakdown; we push the authoritative total as one line.
		"line_items": []map[string]any{
			{"name": "smolbill invoice " + inv.ID, "amount": total.MinorUnits(), "quantity": 1},
		},
		"notes": map[string]string{
			"smolbill_invoice_id":  inv.ID,
			"reconciliation_hash":  req.Hash,
			"smolbill_customer_id": req.Customer.ID,
		},
	}

	var created struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		ShortURL string `json:"short_url"`
	}
	if err := c.post(ctx, "/invoices", body, base+":invoice", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("razorpay: create invoice: %w", err)
	}
	status := created.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: created.ID,
		Status:     status,
		HostedURL:  created.ShortURL,
	}, nil
}

// FetchInvoice reads an invoice back from Razorpay to reconcile across the money
// rail. Razorpay reports amounts in integer minor units, so the comparison against
// smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}
	if err := c.get(ctx, "/invoices/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	return payments.FetchedInvoice{
		AmountMinor: resp.Amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported. A thin Razorpay invoice is collected
// via its hosted short_url; this adapter holds no saved token or mandate to pull a
// payment off-session. We return a transport-style error (not a "failed"
// ChargeResult) so smolbill's internal dunning surfaces this rather than mis-routing
// it as a card decline.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("razorpay: off-session collection requires a saved token/mandate not held by this adapter; disable smolbill dunning for this processor")
}

// post sends a JSON request to Razorpay with HTTP BASIC auth and an idempotency
// header, decoding a 2xx JSON body into out (if non-nil) and turning a non-2xx into
// a structured error.
func (c *Client) post(ctx context.Context, path string, body any, idempotencyKey string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("X-Razorpay-Idempotency", idempotencyKey)
	}
	return c.do(req, out)
}

// get issues an authenticated GET to Razorpay and decodes a 2xx JSON body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
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
			Error struct {
				Description string `json:"description"`
				Code        string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error.Description != "" {
			return fmt.Errorf("razorpay %d: %s (%s)", resp.StatusCode, e.Error.Description, e.Error.Code)
		}
		return fmt.Errorf("razorpay %d: %s", resp.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("razorpay: decode response: %w", err)
		}
	}
	return nil
}
