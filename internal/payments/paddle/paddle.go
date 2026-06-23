// Package paddle is a thin, transparent Paddle (Paddle Billing) adapter
// implementing payments.Processor. Paddle is a Merchant of Record (MoR): it
// collects from the end customer, remits tax, and runs its own card retries
// (dunning). smolbill therefore uses Paddle to *collect a finalized total* and to
// *read back what it billed* for cross-rail reconciliation — it never holds funds
// itself (build plan §2, §16).
//
// Like the Dodo adapter (and unlike Stripe, which itemizes via invoice items),
// Paddle collects a single transaction for the invoice total. That is the correct
// mapping for a product/MoR processor: the deterministic engine owns the line
// breakdown and the reconciliation proof; the processor only needs the
// authoritative total to collect, and FetchInvoice reconciles on that total in
// exact minor units.
//
// No SDK: a small net/http client over Paddle's REST API keeps the single-binary
// promise and makes every field on the wire auditable. Amounts are always sent as
// exact integer minor units (cents) — never a float.
//
// Paddle wraps every successful response in a top-level {"data": {...}} envelope,
// so we decode through a Data field. Field names follow Paddle Billing's
// documented Transactions/Customers API; verify against a live sandbox key before
// production use.
package paddle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
)

const (
	liveBaseURL    = "https://api.paddle.com"
	sandboxBaseURL = "https://sandbox-api.paddle.com"
)

// Client is a minimal Paddle Billing API client.
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

// New builds a Paddle client. apiKey is the secret API key. sandbox selects the
// sandbox API root; false uses the live root.
func New(apiKey string, sandbox bool, opts ...Option) *Client {
	base := liveBaseURL
	if sandbox {
		base = sandboxBaseURL
	}
	c := &Client{apiKey: apiKey, baseURL: base, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// FromEnv builds a Paddle processor from PADDLE_API_KEY. PADDLE_SANDBOX
// ("1"/"true"/"yes"/"on") selects the sandbox API root (default: live). Returns
// ok=false with no error when the key is unset, so the registry can skip to the
// next rail.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("PADDLE_API_KEY")
	if key == "" {
		return nil, false, nil
	}
	sandbox := isTrue(os.Getenv("PADDLE_SANDBOX"))
	return New(key, sandbox), true, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "paddle" }

// PushInvoice creates (idempotently) a customer, then a transaction for the
// invoice total. The reconciliation hash is stamped in custom_data so the Paddle
// record links back to smolbill's proof. Returns the Paddle transaction id,
// status, and hosted checkout url.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	// 1. Customer (reused across pushes via a stable idempotency key).
	//
	// NOTE: live Paddle normally REQUIRES an email to create a customer — a field
	// smolbill's domain.Customer does not carry. We pass name + the smolbill ids in
	// custom_data so the record is still traceable, but this MUST be verified against
	// a live/sandbox key: if Paddle rejects the customer for a missing email, the
	// caller will need to thread an email through PushRequest for this rail.
	custBody := map[string]any{
		"name":        req.Customer.Name,
		"custom_data": map[string]string{"smolbill_customer_id": req.Customer.ID},
	}
	if req.Customer.ExternalID != "" {
		custBody["custom_data"].(map[string]string)["external_id"] = req.Customer.ExternalID
	}
	var cust struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/customers", custBody, "cust:"+req.Customer.ID, &cust); err != nil {
		return payments.PushResult{}, fmt.Errorf("paddle: create customer: %w", err)
	}

	// 2. Transaction for the authoritative total, in integer minor units. Paddle
	// expects the amount as a STRING of integer minor units (cents) — never a float.
	total := money.New(inv.Total, inv.Currency).RoundDown()
	txnBody := map[string]any{
		"customer_id":   cust.Data.ID,
		"currency_code": strings.ToUpper(inv.Currency),
		// A single item carrying the computed total. Paddle's price `amount` is a
		// string of integer minor units; quantity 1 collects exactly that total.
		"items": []map[string]any{
			{
				"quantity": 1,
				"price": map[string]any{
					"description": "smolbill invoice " + inv.ID,
					"unit_price": map[string]any{
						"amount":        strconv.FormatInt(total.MinorUnits(), 10),
						"currency_code": strings.ToUpper(inv.Currency),
					},
				},
			},
		},
		"custom_data": map[string]string{
			"smolbill_invoice_id":  inv.ID,
			"reconciliation_hash":  req.Hash,
			"smolbill_customer_id": req.Customer.ID,
		},
	}
	var created struct {
		Data struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Checkout struct {
				URL string `json:"url"`
			} `json:"checkout"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/transactions", txnBody, base+":txn", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("paddle: create transaction: %w", err)
	}
	status := created.Data.Status
	if status == "" {
		status = "open"
	}
	return payments.PushResult{
		ExternalID: created.Data.ID,
		Status:     status,
		HostedURL:  created.Data.Checkout.URL,
	}, nil
}

// FetchInvoice reads a transaction back from Paddle to reconcile across the money
// rail. Paddle reports the grand total as a decimal STRING inside
// data.details.totals.grand_total.
//
// NOTE: per Paddle Billing's convention this string is in integer MINOR units
// already (e.g. "6400" for $64.00), so we parse it as an integer count of minor
// units — no float ever enters. This minor-unit string convention MUST be verified
// against a live/sandbox transaction before production use; if a given API version
// returns a major-unit decimal (e.g. "64.00"), this parse must switch to
// money.New(decimalParsed, currency).RoundDown().MinorUnits() instead.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Data struct {
			Status       string `json:"status"`
			CurrencyCode string `json:"currency_code"`
			Details      struct {
				Totals struct {
					GrandTotal   string `json:"grand_total"`
					CurrencyCode string `json:"currency_code"`
				} `json:"totals"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/transactions/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	amount, err := strconv.ParseInt(strings.TrimSpace(resp.Data.Details.Totals.GrandTotal), 10, 64)
	if err != nil {
		return payments.FetchedInvoice{}, fmt.Errorf("paddle: parse grand_total %q: %w", resp.Data.Details.Totals.GrandTotal, err)
	}
	currency := resp.Data.Details.Totals.CurrencyCode
	if currency == "" {
		currency = resp.Data.CurrencyCode
	}
	return payments.FetchedInvoice{
		AmountMinor: amount,
		Currency:    strings.ToUpper(currency),
		Status:      resp.Data.Status,
	}, nil
}

// ChargeInvoice is intentionally unsupported for a Merchant-of-Record rail. Paddle
// collects from the customer via the hosted checkout and runs its OWN card retries
// (dunning), so there is no off-session pull for smolbill to drive. We return a
// transport-style error (not a "failed" ChargeResult) so smolbill's internal
// dunning surfaces this rather than mis-routing it as a card decline. For Paddle,
// leave smolbill dunning disabled and let the MoR recover.
func (c *Client) ChargeInvoice(_ context.Context, _ string) (payments.ChargeResult, error) {
	return payments.ChargeResult{}, fmt.Errorf("paddle: collection is managed by the merchant-of-record; disable smolbill dunning for this processor")
}

// post sends a JSON request to Paddle with bearer auth and an idempotency key,
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

// get issues an authenticated GET to Paddle and decodes a 2xx JSON body.
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
		// Paddle errors are wrapped as {"error": {"code": ..., "detail": ...}}.
		var e struct {
			Message string `json:"message"`
			Error   struct {
				Detail string `json:"detail"`
				Code   string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case e.Error.Detail != "":
			return fmt.Errorf("paddle %d: %s (%s)", resp.StatusCode, e.Error.Detail, e.Error.Code)
		case e.Message != "":
			return fmt.Errorf("paddle %d: %s", resp.StatusCode, e.Message)
		default:
			return fmt.Errorf("paddle %d: %s", resp.StatusCode, string(body))
		}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("paddle: decode response: %w", err)
		}
	}
	return nil
}
