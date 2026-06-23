// Package creem is a thin, transparent Creem (creem.io) adapter implementing
// payments.Processor. Creem is a Merchant of Record (MoR) for SaaS/indie devs
// (like Dodo/Polar/Paddle/Lemon Squeezy): it collects from the end customer,
// remits tax, and runs its own card retries. smolbill therefore uses Creem to
// *collect a finalized total* and to *read back what it billed* for cross-rail
// reconciliation — it never holds funds itself (build plan §2, §16).
//
// Like the Dodo adapter (and unlike Stripe's itemized invoices), Creem collects a
// single payment for the invoice total. That is the correct mapping for a
// product/MoR processor: the deterministic engine owns the line breakdown and the
// reconciliation proof; the processor only needs the authoritative total to
// collect, and FetchInvoice reconciles on that total in exact minor units.
//
// No SDK: a small net/http client over Creem's REST API keeps the single-binary
// promise and makes every field on the wire auditable. Amounts are always sent as
// exact integer minor units (cents) — never a float.
//
// NOTE: The KEY DIFFERENCE from Dodo is auth — Creem authenticates via the
// `x-api-key` HEADER, not a Bearer token. The auth header name (`x-api-key`) and
// the base URLs below should be verified against live Creem docs before
// production use.
package creem

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
	// liveBaseURL / testBaseURL — verify against live Creem docs.
	liveBaseURL = "https://api.creem.io"
	testBaseURL = "https://test-api.creem.io"
)

// Client is a minimal Creem API client.
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

// New builds a Creem client. apiKey is the secret API key. test selects the test
// API root; false uses the live root.
func New(apiKey string, test bool, opts ...Option) *Client {
	base := liveBaseURL
	if test {
		base = testBaseURL
	}
	c := &Client{apiKey: apiKey, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Creem processor from CREEM_API_KEY. CREEM_TEST
// ("1"/"true"/"yes"/"on") selects the test API root (default: live). Returns
// ok=false with no error when the key is unset, so the registry can skip to the
// next rail.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("CREEM_API_KEY")
	if key == "" {
		return nil, false, nil
	}
	test := isTrue(os.Getenv("CREEM_TEST"))
	return New(key, test), true, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "creem" }

// PushInvoice creates (idempotently) a Creem checkout for the invoice total, in
// integer minor units. The reconciliation hash is stamped in metadata so the
// Creem record links back to smolbill's proof. Returns the Creem checkout id,
// status, and hosted checkout URL.
//
// NOTE: Creem checkouts are normally product-based; collecting a custom/arbitrary
// amount on a checkout must be verified against a live key before production use.
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
		ID          string `json:"id"`
		CheckoutID  string `json:"checkout_id"`
		Status      string `json:"status"`
		CheckoutURL string `json:"checkout_url"`
	}
	if err := c.post(ctx, "/v1/checkouts", body, base+":checkout", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("creem: create checkout: %w", err)
	}
	id := created.ID
	if id == "" {
		id = created.CheckoutID
	}
	status := created.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: id,
		Status:     status,
		HostedURL:  created.CheckoutURL,
	}, nil
}

// FetchInvoice reads a checkout back from Creem to reconcile across the money
// rail. Creem reports amounts in integer minor units, so the comparison against
// smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}
	if err := c.get(ctx, "/v1/checkouts/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	return payments.FetchedInvoice{
		AmountMinor: resp.Amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported for a Merchant-of-Record rail. Creem
// collects from the customer via the hosted checkout and runs its OWN card retries
// (dunning), so there is no off-session pull for smolbill to drive. We return a
// transport-style error (not a "failed" ChargeResult) so smolbill's internal
// dunning surfaces this rather than mis-routing it as a card decline. For Creem,
// leave smolbill dunning disabled and let the MoR recover.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("creem: collection is managed by the merchant-of-record; disable smolbill dunning for this processor")
}

// post sends a JSON request to Creem with x-api-key auth and an idempotency key,
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
	// Creem auth: x-api-key header (NOT Bearer) — verify name against live docs.
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.do(req, out)
}

// get issues an authenticated GET to Creem and decodes a 2xx JSON body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	// Creem auth: x-api-key header (NOT Bearer) — verify name against live docs.
	req.Header.Set("x-api-key", c.apiKey)
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
			return fmt.Errorf("creem %d: %s (%s)", resp.StatusCode, e.Error.Message, e.Error.Code)
		case e.Message != "":
			return fmt.Errorf("creem %d: %s", resp.StatusCode, e.Message)
		default:
			return fmt.Errorf("creem %d: %s", resp.StatusCode, string(body))
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("creem: decode response: %w", err)
		}
	}
	return nil
}
