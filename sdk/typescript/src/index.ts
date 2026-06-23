// smolbill TypeScript SDK — a thin, typed wrapper over the /v1 HTTP API.
//
// Design: parameters and responses are snake_case, mirroring the wire 1:1 (the
// same choice Stripe's SDK makes). There is no field-mapping layer, so the SDK
// cannot silently disagree with the API — which is the whole smolbill thesis
// applied to the client. Zero runtime dependencies: Node's global fetch + the
// built-in crypto for webhook signature verification.
//
//   const sb = new Smolbill({ baseUrl: "http://localhost:8080" });
//   const cus = await sb.customers.create({ name: "Acme AI" });
//   await sb.events.ingest({ idempotency_key: "e1", customer_id: cus.id,
//                            meter_code: "tokens", properties: { n: 1000 } });

import { createHmac, timingSafeEqual } from "node:crypto";

// ---------- types (mirror internal/domain, snake_case) ----------

export type Aggregation = "count" | "sum" | "max" | "unique";
export type PriceModel = "flat" | "per_unit" | "tiered_graduated" | "tiered_volume";

export interface Customer {
  id: string;
  external_id: string;
  name: string;
  created_at: string;
}

export interface Meter {
  id: string;
  code: string;
  name: string;
  aggregation: Aggregation;
  property_key: string;
  created_at: string;
}

export interface TierInput {
  up_to?: string | null; // decimal string; null/omitted = unbounded final tier
  unit_amount?: string;
  flat_amount?: string;
}

export interface PriceInput {
  meter_code?: string; // omit for a pure flat fee
  model: PriceModel;
  currency: string;
  unit_amount?: string;
  flat_amount?: string;
  tiers?: TierInput[];
}

export interface PlanInput {
  name: string;
  version?: number;
  prices: PriceInput[];
}

export interface Plan {
  id: string;
  name: string;
  version: number;
  prices: unknown[];
  created_at: string;
}

export interface Subscription {
  id: string;
  customer_id: string;
  plan_id: string;
  plan_version: number;
  status: "active" | "paused" | "canceled";
  current_period_start: string;
  current_period_end: string;
  started_at: string;
  canceled_at: string | null;
}

export interface UsageLine {
  meter_code: string;
  value: string;
}

export interface Usage {
  customer_id: string;
  subscription_id: string;
  period_start: string;
  period_end: string;
  usage: UsageLine[];
  projected_total: string;
  currency: string;
  as_of?: string;
}

export interface InvoiceLine {
  meter_code: string;
  model: string;
  raw_event_count: number;
  quantity: string;
  proration_factor: string;
  amount: string;
}

export interface Invoice {
  customer_id: string;
  period_start: string;
  period_end: string;
  lines: InvoiceLine[];
  total: string;
  currency: string;
  hash: string;
  invoice_id?: string;
  status?: string;
  external_invoice_id?: string;
  processor?: string;
}

export interface LineDelta {
  meter_code: string;
  current_amount: string;
  proposed_amount: string;
  delta: string;
}

export interface SimulationResult {
  customer_id: string;
  period_start: string;
  period_end: string;
  currency: string;
  current_total: string;
  proposed_total: string;
  delta: string;
  lines: LineDelta[];
  current_hash: string;
  proposed_hash: string;
}

// Reconcile/verify return a verdict, NOT an error, on disagreement. `consistent`
// reflects the HTTP status (200 = consistent, 409 = drift). The raw body carries
// the line-level diff.
export interface ReconcileResult {
  consistent: boolean;
  [k: string]: unknown;
}

export interface Webhook {
  id: string;
  url: string;
  events: string[];
  created_at: string;
}

export interface CreatedWebhook extends Webhook {
  secret: string; // returned exactly once, here
}

export interface Wallet {
  wallet?: Record<string, unknown>;
  [k: string]: unknown;
}

export type CollectionStatus = "scheduled" | "retrying" | "requires_action" | "paid" | "uncollectible";

export interface Collection {
  invoice_id: string;
  external_invoice_id: string;
  status: CollectionStatus;
  attempts: number;
  first_failed_at: string | null;
  next_attempt_at: string | null;
  last_reason: string;
  currency: string;
  amount_minor: number;
  updated_at: string;
}

export interface DunningRunResult {
  processed: number;
  faults: number;
  by_status: Record<string, number>;
  checked_at: string;
}

export interface DunningAnalytics {
  scheduled: number;
  retrying: number;
  requires_action: number;
  recovered: number;
  uncollectible: number;
  amount_at_risk: Record<string, string>;
  amount_recovered: Record<string, string>;
  recovery_rate: string;
}

export interface Analytics {
  generated_at: string;
  customers: number;
  active_subscriptions: number;
  finalized_invoices: number;
  projected_revenue: Record<string, string>;
  finalized_revenue: Record<string, string>;
  dunning: DunningAnalytics;
}

export type DunningMessageEvent = "payment_failed" | "requires_action" | "recovered" | "uncollectible";

export interface DunningTemplate {
  event: DunningMessageEvent;
  subject: string;
  body: string;
  customized: boolean;
}

export interface WebhookEvent {
  id: string;
  type: string;
  occurred_at: string;
  data: Record<string, unknown>;
}

// ---------- errors ----------

export class SmolbillError extends Error {
  readonly status: number;
  readonly body: unknown;
  constructor(message: string, status: number, body?: unknown) {
    super(message);
    this.name = "SmolbillError";
    this.status = status;
    this.body = body;
  }
}

// ---------- webhook signature verification (the payoff of signed delivery) ----------

/** sign reproduces the X-Smolbill-Signature value: hex HMAC-SHA256 of body. */
export function sign(secret: string, body: string): string {
  return createHmac("sha256", secret).update(body).digest("hex");
}

/**
 * verifyWebhook checks a delivery's signature against the endpoint secret and
 * returns the parsed event. Throws SmolbillError if the signature does not match
 * (constant-time compare) — so a handler can trust event.type and event.data.
 *
 *   app.post("/webhooks", (req, res) => {
 *     const evt = verifyWebhook({ secret: process.env.WHSEC!,
 *       body: req.rawBody, signature: req.header("X-Smolbill-Signature")! });
 *     if (evt.type === "drift.detected") page(evt.data.invoice_id);
 *   });
 */
export function verifyWebhook(opts: { secret: string; body: string; signature: string }): WebhookEvent {
  const expected = sign(opts.secret, opts.body);
  const a = Buffer.from(expected);
  const b = Buffer.from(opts.signature ?? "");
  if (a.length !== b.length || !timingSafeEqual(a, b)) {
    throw new SmolbillError("invalid webhook signature", 0);
  }
  return JSON.parse(opts.body) as WebhookEvent;
}

// ---------- client ----------

export interface SmolbillOptions {
  baseUrl: string;
  /** Optional bearer token, sent as Authorization: Bearer <apiKey> if set. */
  apiKey?: string;
  /** Override fetch (e.g. for tests/older runtimes). Defaults to global fetch. */
  fetch?: typeof fetch;
}

export class Smolbill {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly doFetch: typeof fetch;

  constructor(opts: SmolbillOptions) {
    this.baseUrl = opts.baseUrl.replace(/\/$/, "");
    this.apiKey = opts.apiKey;
    const f = opts.fetch ?? globalThis.fetch;
    if (!f) throw new Error("smolbill: no fetch available; pass options.fetch");
    this.doFetch = f;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (this.apiKey) headers["Authorization"] = `Bearer ${this.apiKey}`;
    const res = await this.doFetch(this.baseUrl + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    const parsed = text ? JSON.parse(text) : undefined;
    if (res.status < 200 || res.status >= 300) {
      const msg = parsed && typeof parsed === "object" && "error" in parsed ? String(parsed.error) : `HTTP ${res.status}`;
      throw new SmolbillError(msg, res.status, parsed);
    }
    return parsed as T;
  }

  // reconcile/verify: a 409 is a drift verdict, not a transport error.
  private async verdict(path: string): Promise<ReconcileResult> {
    const headers: Record<string, string> = {};
    if (this.apiKey) headers["Authorization"] = `Bearer ${this.apiKey}`;
    const res = await this.doFetch(this.baseUrl + path, { method: "GET", headers });
    const text = await res.text();
    const parsed = text ? JSON.parse(text) : {};
    if (res.status !== 200 && res.status !== 409) {
      const msg = parsed && parsed.error ? String(parsed.error) : `HTTP ${res.status}`;
      throw new SmolbillError(msg, res.status, parsed);
    }
    return { ...parsed, consistent: res.status === 200 };
  }

  customers = {
    create: (req: { name: string; external_id?: string }) =>
      this.request<Customer>("POST", "/v1/customers", req),
  };

  meters = {
    create: (req: { code: string; name: string; aggregation: Aggregation; property_key?: string }) =>
      this.request<Meter>("POST", "/v1/meters", req),
  };

  plans = {
    create: (req: PlanInput) => this.request<Plan>("POST", "/v1/plans", req),
  };

  subscriptions = {
    create: (req: {
      customer_id: string;
      plan_id: string;
      period_start: string;
      period_end: string;
      started_at?: string;
    }) => this.request<Subscription>("POST", "/v1/subscriptions", req),
  };

  events = {
    ingest: (req: {
      idempotency_key: string;
      customer_id: string;
      meter_code: string;
      event_time?: string;
      properties: Record<string, unknown>;
    }) => this.request<{ status: string }>("POST", "/v1/events", req),
  };

  usage = {
    get: (customerId: string, opts?: { as_of?: string }) =>
      this.request<Usage>("GET", `/v1/usage/${encodeURIComponent(customerId)}${opts?.as_of ? `?as_of=${encodeURIComponent(opts.as_of)}` : ""}`),
  };

  invoices = {
    preview: (req: { subscription_id: string }) =>
      this.request<Invoice>("POST", "/v1/invoices/preview", req),
    simulate: (req: { customer_id: string; plan: PlanInput }) =>
      this.request<SimulationResult>("POST", "/v1/invoices/simulate", req),
    finalize: (req: { subscription_id: string }) =>
      this.request<Invoice>("POST", "/v1/invoices/finalize", req),
    reconcile: (invoiceId: string) =>
      this.verdict(`/v1/reconcile/${encodeURIComponent(invoiceId)}`),
    verify: (invoiceId: string) =>
      this.verdict(`/v1/invoices/${encodeURIComponent(invoiceId)}/verify`),
    // ASC 606 revenue recognition as of a date (default now): recognized vs deferred.
    revenue: (invoiceId: string, asOf?: string) =>
      this.request<unknown>("GET", `/v1/invoices/${encodeURIComponent(invoiceId)}/revenue${asOf ? `?as_of=${encodeURIComponent(asOf)}` : ""}`),
    // Dunning: attempt collection now, or inspect the recovery state.
    collect: (invoiceId: string) =>
      this.request<Collection>("POST", `/v1/invoices/${encodeURIComponent(invoiceId)}/collect`),
    collection: (invoiceId: string) =>
      this.request<Collection>("GET", `/v1/invoices/${encodeURIComponent(invoiceId)}/collection`),
  };

  dunning = {
    // Process every due collection — the endpoint a cron hits on a cadence.
    run: () => this.request<DunningRunResult>("POST", "/v1/dunning/run"),
    // The customer-facing dunning copy is yours to read, edit, and preview.
    templates: () => this.request<{ templates: DunningTemplate[] }>("GET", "/v1/dunning/templates"),
    setTemplate: (event: DunningMessageEvent, req: { subject: string; body: string }) =>
      this.request<{ event: string; customized: boolean }>("PUT", `/v1/dunning/templates/${encodeURIComponent(event)}`, req),
    preview: (event: DunningMessageEvent, context?: Record<string, unknown>) =>
      this.request<{ event: string; subject: string; body: string }>("POST", "/v1/dunning/preview", { event, context }),
  };

  analytics = {
    // Account-wide snapshot: revenue by currency, subscriptions, dunning recovery.
    get: () => this.request<Analytics>("GET", "/v1/analytics"),
  };

  entitlements = {
    create: (req: {
      customer_id: string;
      feature: string;
      kind: "boolean" | "metered";
      meter_code?: string;
      limit_value?: string;
      period_start?: string;
      period_end?: string;
    }) => this.request<unknown>("POST", "/v1/entitlements", req),
    get: (customerId: string) =>
      this.request<unknown>("GET", `/v1/entitlements/${encodeURIComponent(customerId)}`),
  };

  alerts = {
    create: (req: {
      customer_id: string;
      budget: string;
      currency: string;
      webhook_url: string;
      thresholds?: number[];
    }) => this.request<unknown>("POST", "/v1/alerts", req),
  };

  webhooks = {
    create: (req: { url: string; events?: string[] }) =>
      this.request<CreatedWebhook>("POST", "/v1/webhooks", req),
    list: () => this.request<{ webhooks: Webhook[] }>("GET", "/v1/webhooks"),
  };

  wallet = {
    topup: (customerId: string, req: { amount: string; currency: string; reason?: string; idempotency_key?: string }) =>
      this.request<Wallet>("POST", `/v1/wallet/${encodeURIComponent(customerId)}/topup`, req),
    get: (customerId: string) =>
      this.request<Wallet>("GET", `/v1/wallet/${encodeURIComponent(customerId)}`),
  };

  health = () => this.request<{ status: string }>("GET", "/healthz");

  // Real-time gate: may this customer do this right now? Hard allow/deny against a
  // metered entitlement (feature + quantity) and/or the prepaid wallet (cost).
  gate = (req: { customer_id: string; feature?: string; quantity?: string; cost?: string; currency?: string }) =>
    this.request<{ allowed: boolean; reason: string }>("POST", "/v1/gate", req);
}

export default Smolbill;
