// Package stripe is a thin, transparent Stripe adapter implementing
// payments.Processor. It uses Stripe for invoicing/charges only (build plan §10)
// and deliberately avoids the full SDK: a small net/http client over Stripe's
// REST API keeps the single-binary promise, makes every field on the wire
// auditable, and lets us guarantee amounts are sent as exact integer minor units
// (cents) — never a float.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Arjun0606/smolbill/internal/money"
	"github.com/Arjun0606/smolbill/internal/payments"
)

// defaultBaseURL is Stripe's API root. Overridable for tests via WithBaseURL.
const defaultBaseURL = "https://api.stripe.com"

// FromEnv builds a Stripe processor from STRIPE_SECRET_KEY. The second return is
// false (with no error) when the key is unset, so the provider registry can skip
// Stripe and try the next rail. This is the registry contract every adapter shares.
func FromEnv() (payments.Processor, bool, error) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if key == "" {
		return nil, false, nil
	}
	return New(key, WithWebhookSecret(os.Getenv("STRIPE_WEBHOOK_SECRET"))), true, nil
}

// Client is a minimal Stripe API client.
type Client struct {
	apiKey        string
	baseURL       string
	webhookSecret string
	http          *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (a mock in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithWebhookSecret sets the signing secret (whsec_...) used to verify inbound
// webhooks. Without it, VerifyWebhook rejects everything.
func WithWebhookSecret(s string) Option { return func(c *Client) { c.webhookSecret = s } }

// New builds a Stripe client. apiKey is the secret key (sk_...).
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements payments.Processor.
func (c *Client) Name() string { return "stripe" }

// VerifyWebhook implements payments.InboundWebhooker using Stripe's documented
// signing scheme: the `Stripe-Signature` header carries `t=<timestamp>,v1=<sig>`,
// where sig = HMAC-SHA256(`<t>.<body>`, webhook_secret) in hex. We recompute and
// constant-time compare, then normalize the event to what smolbill's dunning cares
// about. A request that doesn't verify is rejected — the signature is the auth.
func (c *Client) VerifyWebhook(header http.Header, body []byte) (payments.ProcessorEvent, error) {
	if c.webhookSecret == "" {
		return payments.ProcessorEvent{}, fmt.Errorf("stripe: no webhook secret configured (set STRIPE_WEBHOOK_SECRET)")
	}
	sigHeader := header.Get("Stripe-Signature")
	var ts string
	for _, part := range strings.Split(sigHeader, ",") {
		if kv := strings.SplitN(strings.TrimSpace(part), "=", 2); len(kv) == 2 && kv[0] == "t" {
			ts = kv[1]
		}
	}
	if ts == "" {
		return payments.ProcessorEvent{}, fmt.Errorf("stripe: malformed Stripe-Signature header")
	}
	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write([]byte(ts + "." + string(body)))
	expected := hex.EncodeToString(mac.Sum(nil))
	// Stripe may send several v1 signatures (during secret rotation); accept any match.
	verified := false
	for _, part := range strings.Split(sigHeader, ",") {
		if kv := strings.SplitN(strings.TrimSpace(part), "=", 2); len(kv) == 2 && kv[0] == "v1" &&
			subtle.ConstantTimeCompare([]byte(kv[1]), []byte(expected)) == 1 {
			verified = true
			break
		}
	}
	if !verified {
		return payments.ProcessorEvent{}, fmt.Errorf("stripe: webhook signature verification failed")
	}

	var evt struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				Metadata         map[string]string `json:"metadata"`
				FailureCode      string            `json:"failure_code"`
				FailureMessage   string            `json:"failure_message"`
				LastPaymentError *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"last_payment_error"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &evt); err != nil {
		return payments.ProcessorEvent{}, fmt.Errorf("stripe: bad webhook body: %w", err)
	}

	ev := payments.ProcessorEvent{InvoiceID: evt.Data.Object.Metadata["smolbill_invoice_id"]}
	switch evt.Type {
	case "invoice.payment_succeeded", "invoice.paid", "payment_intent.succeeded", "charge.succeeded":
		ev.Kind = "paid"
	case "invoice.payment_failed", "payment_intent.payment_failed", "charge.failed":
		ev.Kind = "failed"
		ev.Reason = firstNonEmpty(evt.Data.Object.FailureCode, evt.Data.Object.FailureMessage)
		if ev.Reason == "" && evt.Data.Object.LastPaymentError != nil {
			ev.Reason = firstNonEmpty(evt.Data.Object.LastPaymentError.Code, evt.Data.Object.LastPaymentError.Message)
		}
	case "invoice.payment_action_required", "payment_intent.requires_action":
		ev.Kind = "action_required"
	default:
		ev.Kind = "ignored"
	}
	return ev, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// PushInvoice creates a Stripe customer (idempotently), an invoice item per
// smolbill line, then an invoice, and finalizes it. Every call carries an
// Idempotency-Key derived from req.IdempotencyKey so retries never duplicate.
func (c *Client) PushInvoice(ctx context.Context, req payments.PushRequest) (payments.PushResult, error) {
	inv := req.Invoice
	base := req.IdempotencyKey
	if base == "" {
		base = inv.ID
	}

	// 1. Customer (reused across pushes via a stable idempotency key).
	custForm := url.Values{}
	custForm.Set("name", req.Customer.Name)
	custForm.Set("metadata[smolbill_customer_id]", req.Customer.ID)
	if req.Customer.ExternalID != "" {
		custForm.Set("metadata[external_id]", req.Customer.ExternalID)
	}
	var cust struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/v1/customers", custForm, "cust:"+req.Customer.ID, &cust); err != nil {
		return payments.PushResult{}, fmt.Errorf("stripe: create customer: %w", err)
	}

	// 2. One invoice item per line, amount in integer minor units.
	for i, line := range inv.Lines {
		amt := money.New(line.Amount, inv.Currency).MinorUnits()
		if amt == 0 {
			continue // nothing to charge for this line
		}
		itemForm := url.Values{}
		itemForm.Set("customer", cust.ID)
		itemForm.Set("amount", strconv.FormatInt(amt, 10))
		itemForm.Set("currency", strings.ToLower(inv.Currency))
		itemForm.Set("description", lineDescription(line.MeterCode))
		itemForm.Set("metadata[smolbill_invoice_id]", inv.ID)
		if err := c.post(ctx, "/v1/invoiceitems", itemForm, fmt.Sprintf("%s:item:%d", base, i), nil); err != nil {
			return payments.PushResult{}, fmt.Errorf("stripe: create invoice item %d: %w", i, err)
		}
	}

	// 3. Invoice over the pending items.
	invForm := url.Values{}
	invForm.Set("customer", cust.ID)
	invForm.Set("collection_method", "charge_automatically")
	invForm.Set("auto_advance", "true")
	invForm.Set("metadata[smolbill_invoice_id]", inv.ID)
	if req.Hash != "" {
		invForm.Set("metadata[reconciliation_hash]", req.Hash)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/v1/invoices", invForm, base+":invoice", &created); err != nil {
		return payments.PushResult{}, fmt.Errorf("stripe: create invoice: %w", err)
	}

	// 4. Finalize to lock numbers and get a hosted URL + concrete status.
	var fin struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		HostedInvoiceURL string `json:"hosted_invoice_url"`
	}
	if err := c.post(ctx, "/v1/invoices/"+created.ID+"/finalize", url.Values{}, base+":finalize", &fin); err != nil {
		return payments.PushResult{}, fmt.Errorf("stripe: finalize invoice: %w", err)
	}

	return payments.PushResult{
		ExternalID: fin.ID,
		Status:     fin.Status,
		HostedURL:  fin.HostedInvoiceURL,
	}, nil
}

// FetchInvoice reads an invoice back from Stripe to reconcile across the money
// rail. Stripe already reports amounts in integer minor units, so the comparison
// against smolbill's ledger is exact — no float ever enters.
func (c *Client) FetchInvoice(ctx context.Context, externalID string) (payments.FetchedInvoice, error) {
	var resp struct {
		Total     int64  `json:"total"`
		AmountDue int64  `json:"amount_due"`
		Currency  string `json:"currency"`
		Status    string `json:"status"`
	}
	if err := c.get(ctx, "/v1/invoices/"+externalID, &resp); err != nil {
		return payments.FetchedInvoice{}, err
	}
	amount := resp.Total
	if amount == 0 {
		amount = resp.AmountDue
	}
	return payments.FetchedInvoice{
		AmountMinor: amount,
		Currency:    strings.ToUpper(resp.Currency),
		Status:      resp.Status,
	}, nil
}

// ChargeInvoice attempts an off-session payment of an existing Stripe invoice
// (POST /v1/invoices/{id}/pay). A card decline (HTTP 402, type "card_error") is
// returned as a failed ChargeResult carrying the granular decline_code — NOT an
// error — so the dunning engine can route by reason. Genuine faults (network,
// 5xx, auth) return a non-nil error so the call itself can be retried later.
func (c *Client) ChargeInvoice(ctx context.Context, externalID string) (payments.ChargeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/invoices/"+externalID+"/pay", strings.NewReader(""))
	if err != nil {
		return payments.ChargeResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return payments.ChargeResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var ok struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(body, &ok)
		status := ok.Status
		if status == "" {
			status = "paid"
		}
		return payments.ChargeResult{Status: status}, nil
	}

	var e struct {
		Error struct {
			Type        string `json:"type"`
			Code        string `json:"code"`
			DeclineCode string `json:"decline_code"`
			Message     string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	// A card decline is an expected dunning outcome, not a transport failure: the
	// decline_code (e.g. "insufficient_funds", "expired_card") is what dunning
	// routes on; fall back to the generic code if Stripe omits it.
	if e.Error.Type == "card_error" {
		reason := e.Error.DeclineCode
		if reason == "" {
			reason = e.Error.Code
		}
		return payments.ChargeResult{Status: "failed", FailureReason: reason}, nil
	}
	if e.Error.Message != "" {
		return payments.ChargeResult{}, fmt.Errorf("stripe %d: %s (%s)", resp.StatusCode, e.Error.Message, e.Error.Type)
	}
	return payments.ChargeResult{}, fmt.Errorf("stripe %d: %s", resp.StatusCode, string(body))
}

// post sends a form-encoded request to Stripe with bearer auth and an
// idempotency key, decoding a 2xx JSON body into out (if non-nil) and turning a
// non-2xx into a structured error.
func (c *Client) post(ctx context.Context, path string, form url.Values, idempotencyKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error.Message != "" {
			return fmt.Errorf("stripe %d: %s (%s)", resp.StatusCode, e.Error.Message, e.Error.Type)
		}
		return fmt.Errorf("stripe %d: %s", resp.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("stripe: decode response: %w", err)
		}
	}
	return nil
}

// get issues an authenticated GET to Stripe and decodes a 2xx JSON body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stripe %d: %s", resp.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("stripe: decode response: %w", err)
		}
	}
	return nil
}

func lineDescription(meterCode string) string {
	if meterCode == "" {
		return "Base subscription fee"
	}
	return "Usage: " + meterCode
}
