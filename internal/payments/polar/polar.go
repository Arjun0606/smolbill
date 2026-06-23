// Package polar is a thin, transparent Polar (polar.sh) adapter implementing
// payments.Processor. Like Dodo, Polar is a Merchant of Record (MoR) for
// developers: it collects from the end customer, remits tax, and runs its own
// card retries. smolbill therefore uses Polar to *collect a finalized total* and
// to *read back what it billed* for cross-rail reconciliation — it never holds
// funds itself (build plan §2, §16).
//
// Unlike the Stripe adapter (which itemizes via invoice items), Polar collects a
// single payment for the invoice total. That is the correct mapping for a
// product/MoR processor: the deterministic engine owns the line breakdown and the
// reconciliation proof; the processor only needs the authoritative total to
// collect, and FetchInvoice reconciles on that total in exact minor units.
//
// No SDK: a small net/http client over Polar's REST API keeps the single-binary
// promise and makes every field on the wire auditable. Amounts are always sent as
// exact integer minor units (cents) — never a float.
//
// NOTE: Polar checkouts are normally PRODUCT/PRICE based — you reference an
// existing product/price rather than naming an arbitrary amount. Driving a custom
// amount for an arbitrary computed total (the "amount" field on the checkout
// create body) must be verified against a live Polar org token before production
// use; the field set here follows Polar's documented Checkouts API.
package polar

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
	liveBaseURL    = "https://api.polar.sh"
	sandboxBaseURL = "https://sandbox-api.polar.sh"
)

// Client is a minimal Polar API client.
type Client struct {
	accessToken string
	baseURL     string
	http        *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (a mock in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Polar client. accessToken is the organization access token.
// sandbox selects the sandbox API root; false uses the live root.
func New(accessToken string, sandbox bool, opts ...Option) *Client {
	base := liveBaseURL
	if sandbox {
		base = sandboxBaseURL
	}
	c := &Client{accessToken: accessToken, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Polar processor from POLAR_ACCESS_TOKEN. POLAR_SANDBOX
// ("1"/"true") selects the sandbox API root (default: live). Returns ok=false with
// no error when the token is unset, so the registry can skip to the next rail.
func FromEnv() (payments.Processor, bool, error) {
	token := os.Getenv("POLAR_ACCESS_TOKEN")
	if token == "" {
		return nil, false, nil
	}
	sandbox := isTrue(os.Getenv("POLAR_SANDBOX"))
	return New(token, sandbox), true, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "polar" }

// PushInvoice creates a Polar checkout for the invoice total, in integer minor
// units. The reconciliation hash is stamped in metadata so the Polar record links
// back to smolbill's proof. Returns the Polar checkout id, status, and hosted
// checkout URL.
//
// NOTE: Polar checkouts are normally product/price based — using a custom "amount"
// for an arbitrary computed total must be verified against a live Polar org token.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	// Checkout for the authoritative total, in integer minor units (cents).
	total := money.New(inv.Total, inv.Currency).RoundDown()
	body := map[string]any{
		"amount":   total.MinorUnits(),
		"currency": strings.ToUpper(inv.Currency),
		"metadata": map[string]string{
			"smolbill_invoice_id":  inv.ID,
			"reconciliation_hash":  req.Hash,
			"smolbill_customer_id": req.Customer.ID,
		},
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := c.post(ctx, "/v1/checkouts/", body, base+":checkout", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("polar: create checkout: %w", err)
	}
	status := created.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: created.ID,
		Status:     status,
		HostedURL:  created.URL,
	}, nil
}

// FetchInvoice reads an order back from Polar to reconcile across the money rail.
// Polar reports amounts in integer minor units, so the comparison against
// smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}
	if err := c.get(ctx, "/v1/orders/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	return payments.FetchedInvoice{
		AmountMinor: resp.Amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported for a Merchant-of-Record rail. Polar
// collects from the customer via the hosted checkout and runs its OWN card retries
// (dunning), so there is no off-session pull for smolbill to drive. We return a
// transport-style error (not a "failed" ChargeResult) so smolbill's internal
// dunning surfaces this rather than mis-routing it as a card decline. For Polar,
// leave smolbill dunning disabled and let the MoR recover.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("polar: collection is managed by the merchant-of-record; disable smolbill dunning for this processor")
}

// post sends a JSON request to Polar with bearer auth and an idempotency key,
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
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.do(req, out)
}

// get issues an authenticated GET to Polar and decodes a 2xx JSON body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
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
		// Polar errors come back variously as {"error": "..."} / {"error": {...}}
		// or {"detail": "..."} / {"detail": [{"msg": ...}]}; decode defensively
		// for both shapes and fall back to the raw body.
		var e struct {
			Error  json.RawMessage `json:"error"`
			Detail json.RawMessage `json:"detail"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case len(e.Error) > 0 && string(e.Error) != "null":
			return fmt.Errorf("polar %d: %s", resp.StatusCode, rawMessage(e.Error))
		case len(e.Detail) > 0 && string(e.Detail) != "null":
			return fmt.Errorf("polar %d: %s", resp.StatusCode, rawMessage(e.Detail))
		default:
			return fmt.Errorf("polar %d: %s", resp.StatusCode, string(body))
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("polar: decode response: %w", err)
		}
	}
	return nil
}

// rawMessage renders a JSON error field as a readable string: a JSON string is
// unquoted; any other shape (object/array) is returned as its raw JSON text.
func rawMessage(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
