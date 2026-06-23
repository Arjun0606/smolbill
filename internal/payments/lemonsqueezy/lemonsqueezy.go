// Package lemonsqueezy is a thin, transparent Lemon Squeezy adapter implementing
// payments.Processor. Lemon Squeezy is a Merchant of Record (MoR): it collects
// from the end customer, remits tax, and runs its own card retries. smolbill
// therefore uses Lemon Squeezy to *collect a finalized total* and to *read back
// what it billed* for cross-rail reconciliation — it never holds funds itself
// (build plan §2, §16).
//
// Like the Dodo adapter (and unlike Stripe's per-line invoice items), Lemon
// Squeezy collects a single payment for the invoice total. That is the correct
// mapping for a product/MoR processor: the deterministic engine owns the line
// breakdown and the reconciliation proof; the processor only needs the
// authoritative total to collect, and FetchInvoice reconciles on that total in
// exact minor units.
//
// No SDK: a small net/http client over Lemon Squeezy's REST API keeps the
// single-binary promise and makes every field on the wire auditable. Lemon
// Squeezy speaks the JSON:API spec — requests and responses are documents shaped
// {"data":{"type":..., "attributes":{...}}} and carry the vnd.api+json media
// type. Amounts are always sent as exact integer minor units (cents) — never a
// float.
//
// NOTE: Lemon Squeezy's catalog model is variant/store based; an arbitrary-amount
// collection maps to a custom-price Checkout tied to the store. Field names follow
// Lemon Squeezy's documented Checkouts/Orders API; verify the custom-price checkout
// path against a live store key before production use.
package lemonsqueezy

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

const defaultBaseURL = "https://api.lemonsqueezy.com/v1"

// Client is a minimal Lemon Squeezy API client.
type Client struct {
	apiKey  string
	storeID string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (a mock in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Lemon Squeezy client. apiKey is the secret API key; storeID is the
// store the checkout is created under.
func New(apiKey, storeID string, opts ...Option) *Client {
	c := &Client{apiKey: apiKey, storeID: storeID, baseURL: defaultBaseURL, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Lemon Squeezy processor from LEMONSQUEEZY_API_KEY and
// LEMONSQUEEZY_STORE_ID. Returns ok=false with no error when the key is unset, so
// the registry can skip to the next rail.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("LEMONSQUEEZY_API_KEY")
	if key == "" {
		return nil, false, nil
	}
	storeID := os.Getenv("LEMONSQUEEZY_STORE_ID")
	return New(key, storeID), true, nil
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "lemonsqueezy" }

// PushInvoice creates a custom-price Checkout for the invoice total. The
// reconciliation hash is stamped in checkout custom data so the Lemon Squeezy
// record links back to smolbill's proof. Returns the Lemon Squeezy checkout id,
// status, and hosted checkout URL.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	// Custom-price checkout for the authoritative total, in integer minor units.
	// Lemon Squeezy is variant/store based; using a custom-price checkout for an
	// arbitrary amount must be verified against a live store key.
	total := money.New(inv.Total, inv.Currency).RoundDown()
	body := map[string]any{
		"data": map[string]any{
			"type": "checkouts",
			"attributes": map[string]any{
				// custom_price is in cents — exact integer minor units, never a float.
				"custom_price": total.MinorUnits(),
				"checkout_data": map[string]any{
					"custom": map[string]string{
						"smolbill_invoice_id":  inv.ID,
						"reconciliation_hash":  req.Hash,
						"smolbill_customer_id": req.Customer.ID,
					},
				},
			},
			"relationships": map[string]any{
				"store": map[string]any{
					"data": map[string]string{"type": "stores", "id": c.storeID},
				},
			},
		},
	}
	var created struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				URL    string `json:"url"`
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/checkouts", body, base+":checkout", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("lemonsqueezy: create checkout: %w", err)
	}
	status := created.Data.Attributes.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: created.Data.ID,
		Status:     status,
		HostedURL:  created.Data.Attributes.URL,
	}, nil
}

// FetchInvoice reads an order back from Lemon Squeezy to reconcile across the
// money rail. Lemon Squeezy reports the order total in integer minor units
// (cents), so the comparison against smolbill's ledger is exact — no float ever
// enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Data struct {
			Attributes struct {
				Total    int64  `json:"total"`
				Currency string `json:"currency"`
				Status   string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/orders/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	return payments.FetchedInvoice{
		AmountMinor: resp.Data.Attributes.Total,
		Currency:    strings.ToUpper(resp.Data.Attributes.Currency),
		Status:      resp.Data.Attributes.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported for a Merchant-of-Record rail. Lemon
// Squeezy collects from the customer via the hosted checkout and runs its OWN card
// retries (dunning), so there is no off-session pull for smolbill to drive. We
// return a transport-style error (not a "failed" ChargeResult) so smolbill's
// internal dunning surfaces this rather than mis-routing it as a card decline.
// For Lemon Squeezy, leave smolbill dunning disabled and let the MoR recover.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("lemonsqueezy: collection is managed by the merchant-of-record; disable smolbill dunning for this processor")
}

// post sends a JSON:API request to Lemon Squeezy with bearer auth and an
// idempotency key, decoding a 2xx JSON:API body into out (if non-nil) and turning
// a non-2xx into a structured error.
func (c *Client) post(ctx context.Context, path string, body any, idempotencyKey string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/vnd.api+json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.do(req, out)
}

// get issues an authenticated JSON:API GET to Lemon Squeezy and decodes a 2xx body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	return c.do(req, out)
}

// setHeaders applies the JSON:API auth and media-type headers Lemon Squeezy
// requires on every request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/vnd.api+json")
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Lemon Squeezy errors are JSON:API: {"errors":[{"detail":...,"title":...}]}.
		var e struct {
			Errors []struct {
				Detail string `json:"detail"`
				Title  string `json:"title"`
				Status string `json:"status"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(body, &e)
		if len(e.Errors) > 0 {
			first := e.Errors[0]
			switch {
			case first.Detail != "":
				return fmt.Errorf("lemonsqueezy %d: %s", resp.StatusCode, first.Detail)
			case first.Title != "":
				return fmt.Errorf("lemonsqueezy %d: %s", resp.StatusCode, first.Title)
			}
		}
		return fmt.Errorf("lemonsqueezy %d: %s", resp.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("lemonsqueezy: decode response: %w", err)
		}
	}
	return nil
}
