// Package postgres is the production store.Store backend: Postgres-only, no
// Kafka/ClickHouse/Temporal (build plan §6 #6, the simplicity wedge). It keeps
// the single-binary promise — pgx is a pure-Go driver and the schema is embedded
// and applied on connect.
//
// Money is stored as NUMERIC and moved across the wire as text (::numeric on the
// way in, ::text on the way out) so no float ever touches a monetary value —
// the same no-float discipline the money package enforces in Go.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/id"
)

//go:embed schema.sql
var schemaSQL string

// Store is the Postgres-backed implementation of store.Store.
type Store struct {
	pool *pgxpool.Pool
	ctx  context.Context
}

// New connects to Postgres, applies the embedded schema (idempotent), and
// returns a ready store. The caller owns the context lifetime.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: apply schema: %w", err)
	}
	return &Store{pool: pool, ctx: ctx}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// --- ingest ---

func (s *Store) SeenKey(key string, now time.Time, window time.Duration) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(s.ctx,
		`SELECT EXISTS (SELECT 1 FROM events WHERE idempotency_key = $1 AND ingested_at >= $2)`,
		key, now.Add(-window),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres: SeenKey: %w", err)
	}
	return exists, nil
}

func (s *Store) AppendEvent(e domain.Event) error {
	if e.ID == "" {
		e.ID = id.New("evt")
	}
	props, err := json.Marshal(e.Properties)
	if err != nil {
		return fmt.Errorf("postgres: marshal properties: %w", err)
	}
	_, err = s.pool.Exec(s.ctx,
		`INSERT INTO events (id, idempotency_key, customer_id, meter_code, event_time, properties, ingested_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.ID, e.IdempotencyKey, e.CustomerID, e.MeterCode, e.EventTime, props, e.IngestedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: AppendEvent: %w", err)
	}
	return nil
}

// --- customers ---

func (s *Store) PutCustomer(c domain.Customer) error {
	if c.ID == "" {
		c.ID = id.New("cus")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO customers (id, external_id, name, created_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (id) DO UPDATE SET external_id = EXCLUDED.external_id, name = EXCLUDED.name`,
		c.ID, nullStr(c.ExternalID), c.Name, c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: PutCustomer: %w", err)
	}
	return nil
}

func (s *Store) GetCustomer(cid string) (domain.Customer, bool, error) {
	var c domain.Customer
	var ext *string
	err := s.pool.QueryRow(s.ctx,
		`SELECT id, external_id, name, created_at FROM customers WHERE id = $1`, cid,
	).Scan(&c.ID, &ext, &c.Name, &c.CreatedAt)
	if err == pgx.ErrNoRows {
		return domain.Customer{}, false, nil
	}
	if err != nil {
		return domain.Customer{}, false, fmt.Errorf("postgres: GetCustomer: %w", err)
	}
	c.ExternalID = derefStr(ext)
	return c, true, nil
}

// --- meters ---

func (s *Store) PutMeter(m domain.Meter) error {
	if m.ID == "" {
		m.ID = id.New("mtr")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO meters (id, code, name, aggregation, property_key, created_at) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, aggregation = EXCLUDED.aggregation, property_key = EXCLUDED.property_key`,
		m.ID, m.Code, m.Name, string(m.Aggregation), nullStr(m.PropertyKey), m.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: PutMeter: %w", err)
	}
	return nil
}

func (s *Store) Meters() (map[string]domain.Meter, error) {
	rows, err := s.pool.Query(s.ctx, `SELECT id, code, name, aggregation, property_key, created_at FROM meters`)
	if err != nil {
		return nil, fmt.Errorf("postgres: Meters: %w", err)
	}
	defer rows.Close()
	out := map[string]domain.Meter{}
	for rows.Next() {
		var m domain.Meter
		var agg string
		var prop *string
		if err := rows.Scan(&m.ID, &m.Code, &m.Name, &agg, &prop, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan meter: %w", err)
		}
		m.Aggregation = domain.Aggregation(agg)
		m.PropertyKey = derefStr(prop)
		out[m.Code] = m
	}
	return out, rows.Err()
}

// --- plans + prices ---

func (s *Store) PutPlan(p domain.Plan) error {
	if p.ID == "" {
		p.ID = id.New("plan")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return fmt.Errorf("postgres: PutPlan begin: %w", err)
	}
	defer tx.Rollback(s.ctx)

	if _, err := tx.Exec(s.ctx,
		`INSERT INTO plans (id, name, version, created_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (id) DO NOTHING`,
		p.ID, p.Name, p.Version, p.CreatedAt,
	); err != nil {
		return fmt.Errorf("postgres: PutPlan: %w", err)
	}
	// Replace prices for this plan (plans are immutable once published; this
	// supports building one up before publish).
	if _, err := tx.Exec(s.ctx, `DELETE FROM prices WHERE plan_id = $1`, p.ID); err != nil {
		return fmt.Errorf("postgres: clear prices: %w", err)
	}
	for _, pr := range p.Prices {
		if pr.ID == "" {
			pr.ID = id.New("price")
		}
		var tiers []byte
		if len(pr.Tiers) > 0 {
			tiers, err = json.Marshal(pr.Tiers)
			if err != nil {
				return fmt.Errorf("postgres: marshal tiers: %w", err)
			}
		}
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO prices (id, plan_id, meter_code, model, currency, unit_amount, flat_amount, tiers)
			 VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8)`,
			pr.ID, p.ID, nullStr(pr.MeterCode), string(pr.Model), pr.Currency,
			pr.UnitAmount.String(), pr.FlatAmount.String(), nullJSON(tiers),
		); err != nil {
			return fmt.Errorf("postgres: insert price: %w", err)
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) GetPlan(pid string) (domain.Plan, bool, error) {
	var p domain.Plan
	err := s.pool.QueryRow(s.ctx,
		`SELECT id, name, version, created_at FROM plans WHERE id = $1`, pid,
	).Scan(&p.ID, &p.Name, &p.Version, &p.CreatedAt)
	if err == pgx.ErrNoRows {
		return domain.Plan{}, false, nil
	}
	if err != nil {
		return domain.Plan{}, false, fmt.Errorf("postgres: GetPlan: %w", err)
	}
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, meter_code, model, currency, unit_amount::text, flat_amount::text, tiers
		 FROM prices WHERE plan_id = $1 ORDER BY id`, pid)
	if err != nil {
		return domain.Plan{}, false, fmt.Errorf("postgres: GetPlan prices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pr domain.Price
		var meterCode, model, currency *string
		var unit, flat *string
		var tiers []byte
		if err := rows.Scan(&pr.ID, &meterCode, &model, &currency, &unit, &flat, &tiers); err != nil {
			return domain.Plan{}, false, fmt.Errorf("postgres: scan price: %w", err)
		}
		pr.PlanID = pid
		pr.MeterCode = derefStr(meterCode)
		pr.Model = domain.PriceModel(derefStr(model))
		pr.Currency = derefStr(currency)
		pr.UnitAmount = parseDec(unit)
		pr.FlatAmount = parseDec(flat)
		if len(tiers) > 0 {
			if err := json.Unmarshal(tiers, &pr.Tiers); err != nil {
				return domain.Plan{}, false, fmt.Errorf("postgres: unmarshal tiers: %w", err)
			}
		}
		p.Prices = append(p.Prices, pr)
	}
	return p, true, rows.Err()
}

// --- subscriptions ---

func (s *Store) PutSubscription(sub domain.Subscription) error {
	if sub.ID == "" {
		sub.ID = id.New("sub")
	}
	if sub.StartedAt.IsZero() {
		sub.StartedAt = sub.CurrentPeriodStart
	}
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO subscriptions (id, customer_id, plan_id, plan_version, status, current_period_start, current_period_end, started_at, canceled_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, plan_id = EXCLUDED.plan_id,
		   plan_version = EXCLUDED.plan_version, current_period_start = EXCLUDED.current_period_start,
		   current_period_end = EXCLUDED.current_period_end, canceled_at = EXCLUDED.canceled_at`,
		sub.ID, sub.CustomerID, sub.PlanID, sub.PlanVersion, string(sub.Status),
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd, sub.StartedAt, sub.CanceledAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: PutSubscription: %w", err)
	}
	return nil
}

func (s *Store) scanSub(row pgx.Row) (domain.Subscription, error) {
	var sub domain.Subscription
	var status string
	err := row.Scan(&sub.ID, &sub.CustomerID, &sub.PlanID, &sub.PlanVersion, &status,
		&sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.StartedAt, &sub.CanceledAt)
	if err != nil {
		return domain.Subscription{}, err
	}
	sub.Status = domain.SubStatus(status)
	return sub, nil
}

const subCols = `id, customer_id, plan_id, plan_version, status, current_period_start, current_period_end, started_at, canceled_at`

func (s *Store) GetSubscription(sid string) (domain.Subscription, bool, error) {
	sub, err := s.scanSub(s.pool.QueryRow(s.ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = $1`, sid))
	if err == pgx.ErrNoRows {
		return domain.Subscription{}, false, nil
	}
	if err != nil {
		return domain.Subscription{}, false, fmt.Errorf("postgres: GetSubscription: %w", err)
	}
	return sub, true, nil
}

func (s *Store) SubscriptionsForCustomer(customerID string) ([]domain.Subscription, error) {
	rows, err := s.pool.Query(s.ctx, `SELECT `+subCols+` FROM subscriptions WHERE customer_id = $1`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: SubscriptionsForCustomer: %w", err)
	}
	defer rows.Close()
	var out []domain.Subscription
	for rows.Next() {
		sub, err := s.scanSub(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan sub: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// --- events ---

func (s *Store) EventsForCustomer(customerID string) ([]domain.Event, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, idempotency_key, customer_id, meter_code, event_time, properties, ingested_at
		 FROM events WHERE customer_id = $1 ORDER BY event_time`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: EventsForCustomer: %w", err)
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var props []byte
		if err := rows.Scan(&e.ID, &e.IdempotencyKey, &e.CustomerID, &e.MeterCode, &e.EventTime, &props, &e.IngestedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan event: %w", err)
		}
		if len(props) > 0 {
			if err := json.Unmarshal(props, &e.Properties); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal properties: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- invoices + reconciliation ledger ---

func (s *Store) SaveFinalizedInvoice(inv domain.Invoice, ledger []domain.LedgerRow) error {
	if inv.ID == "" {
		inv.ID = id.New("inv")
	}
	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return fmt.Errorf("postgres: SaveFinalizedInvoice begin: %w", err)
	}
	defer tx.Rollback(s.ctx)

	if _, err := tx.Exec(s.ctx,
		`INSERT INTO invoices (id, customer_id, subscription_id, period_start, period_end, status, stripe_invoice_id, total, currency)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9)`,
		inv.ID, inv.CustomerID, inv.SubscriptionID, inv.PeriodStart, inv.PeriodEnd,
		inv.Status, nullStr(inv.StripeInvoiceID), inv.Total.String(), inv.Currency,
	); err != nil {
		return fmt.Errorf("postgres: insert invoice: %w", err)
	}
	for _, l := range inv.Lines {
		lineID := l.ID
		if lineID == "" {
			lineID = id.New("line")
		}
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO invoice_lines (id, invoice_id, meter_code, quantity, unit_price, amount, ledger_ref)
			 VALUES ($1,$2,$3,$4::numeric,$5::numeric,$6::numeric,$7)`,
			lineID, inv.ID, nullStr(l.MeterCode), l.Quantity.String(), l.UnitPrice.String(), l.Amount.String(), nullStr(l.LedgerRef),
		); err != nil {
			return fmt.Errorf("postgres: insert invoice line: %w", err)
		}
	}
	for _, r := range ledger {
		rowID := r.ID
		if rowID == "" {
			rowID = id.New("ldg")
		}
		computedAt := r.ComputedAt
		if computedAt.IsZero() {
			computedAt = time.Now().UTC()
		}
		var diffs []byte
		if len(r.Diffs) > 0 {
			diffs, _ = json.Marshal(r.Diffs)
		}
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO reconciliation_ledger (id, invoice_id, meter_code, raw_event_count, meter_value, entitlement_state, invoice_line_amount, diffs, computed_hash, computed_at)
			 VALUES ($1,$2,$3,$4,$5::numeric,$6,$7::numeric,$8,$9,$10)`,
			rowID, inv.ID, nullStr(r.MeterCode), r.RawEventCount, r.MeterValue.String(), nil, r.InvoiceLineAmount.String(),
			nullJSON(diffs), r.ComputedHash, computedAt,
		); err != nil {
			return fmt.Errorf("postgres: insert ledger row: %w", err)
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) GetInvoice(invID string) (domain.Invoice, bool, error) {
	var inv domain.Invoice
	var stripeID *string
	var total *string
	err := s.pool.QueryRow(s.ctx,
		`SELECT id, customer_id, subscription_id, period_start, period_end, status, stripe_invoice_id, total::text, currency
		 FROM invoices WHERE id = $1`, invID,
	).Scan(&inv.ID, &inv.CustomerID, &inv.SubscriptionID, &inv.PeriodStart, &inv.PeriodEnd,
		&inv.Status, &stripeID, &total, &inv.Currency)
	if err == pgx.ErrNoRows {
		return domain.Invoice{}, false, nil
	}
	if err != nil {
		return domain.Invoice{}, false, fmt.Errorf("postgres: GetInvoice: %w", err)
	}
	inv.StripeInvoiceID = derefStr(stripeID)
	inv.Total = parseDec(total)

	rows, err := s.pool.Query(s.ctx,
		`SELECT id, meter_code, quantity::text, unit_price::text, amount::text, ledger_ref
		 FROM invoice_lines WHERE invoice_id = $1 ORDER BY id`, invID)
	if err != nil {
		return domain.Invoice{}, false, fmt.Errorf("postgres: GetInvoice lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.InvoiceLine
		var meterCode, qty, unit, amt, ledgerRef *string
		if err := rows.Scan(&l.ID, &meterCode, &qty, &unit, &amt, &ledgerRef); err != nil {
			return domain.Invoice{}, false, fmt.Errorf("postgres: scan line: %w", err)
		}
		l.MeterCode = derefStr(meterCode)
		l.Quantity = parseDec(qty)
		l.UnitPrice = parseDec(unit)
		l.Amount = parseDec(amt)
		l.LedgerRef = derefStr(ledgerRef)
		inv.Lines = append(inv.Lines, l)
	}
	return inv, true, rows.Err()
}

func (s *Store) GetLedger(invoiceID string) ([]domain.LedgerRow, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, invoice_id, meter_code, raw_event_count, meter_value::text, invoice_line_amount::text, diffs, computed_hash, computed_at
		 FROM reconciliation_ledger WHERE invoice_id = $1 ORDER BY id`, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: GetLedger: %w", err)
	}
	defer rows.Close()
	var out []domain.LedgerRow
	for rows.Next() {
		var r domain.LedgerRow
		var meterCode, mv, amt *string
		var diffs []byte
		if err := rows.Scan(&r.ID, &r.InvoiceID, &meterCode, &r.RawEventCount, &mv, &amt, &diffs, &r.ComputedHash, &r.ComputedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan ledger: %w", err)
		}
		r.MeterCode = derefStr(meterCode)
		r.MeterValue = parseDec(mv)
		r.InvoiceLineAmount = parseDec(amt)
		if len(diffs) > 0 {
			_ = json.Unmarshal(diffs, &r.Diffs)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- entitlements ---

func (s *Store) PutEntitlement(e domain.Entitlement) error {
	if e.ID == "" {
		e.ID = id.New("ent")
	}
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO entitlements (id, customer_id, feature, kind, meter_code, limit_value, used_value, period_start, period_end)
		 VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET limit_value = EXCLUDED.limit_value, meter_code = EXCLUDED.meter_code,
		   period_start = EXCLUDED.period_start, period_end = EXCLUDED.period_end`,
		e.ID, e.CustomerID, e.Feature, string(e.Kind), nullStr(e.MeterCode),
		e.LimitValue.String(), e.UsedValue.String(), nullTime(e.PeriodStart), nullTime(e.PeriodEnd),
	)
	if err != nil {
		return fmt.Errorf("postgres: PutEntitlement: %w", err)
	}
	return nil
}

func (s *Store) EntitlementsForCustomer(customerID string) ([]domain.Entitlement, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, customer_id, feature, kind, meter_code, limit_value::text, used_value::text, period_start, period_end
		 FROM entitlements WHERE customer_id = $1 ORDER BY feature`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: EntitlementsForCustomer: %w", err)
	}
	defer rows.Close()
	var out []domain.Entitlement
	for rows.Next() {
		var e domain.Entitlement
		var kind string
		var meterCode, limit, used *string
		var ps, pe *time.Time
		if err := rows.Scan(&e.ID, &e.CustomerID, &e.Feature, &kind, &meterCode, &limit, &used, &ps, &pe); err != nil {
			return nil, fmt.Errorf("postgres: scan entitlement: %w", err)
		}
		e.Kind = domain.EntitlementKind(kind)
		e.MeterCode = derefStr(meterCode)
		e.LimitValue = parseDec(limit)
		e.UsedValue = parseDec(used)
		if ps != nil {
			e.PeriodStart = *ps
		}
		if pe != nil {
			e.PeriodEnd = *pe
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- alerts ---

func (s *Store) PutAlert(a domain.Alert) error {
	if a.ID == "" {
		a.ID = id.New("alert")
	}
	thresholds, err := json.Marshal(a.Thresholds)
	if err != nil {
		return fmt.Errorf("postgres: marshal thresholds: %w", err)
	}
	_, err = s.pool.Exec(s.ctx,
		`INSERT INTO alerts (id, customer_id, budget, currency, thresholds, webhook_url, max_fired)
		 VALUES ($1,$2,$3::numeric,$4,$5,$6,$7)
		 ON CONFLICT (id) DO UPDATE SET budget = EXCLUDED.budget, thresholds = EXCLUDED.thresholds,
		   webhook_url = EXCLUDED.webhook_url`,
		a.ID, a.CustomerID, a.Budget.String(), a.Currency, thresholds, a.WebhookURL, a.MaxFired,
	)
	if err != nil {
		return fmt.Errorf("postgres: PutAlert: %w", err)
	}
	return nil
}

func (s *Store) AlertsForCustomer(customerID string) ([]domain.Alert, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, customer_id, budget::text, currency, thresholds, webhook_url, max_fired
		 FROM alerts WHERE customer_id = $1 ORDER BY id`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: AlertsForCustomer: %w", err)
	}
	defer rows.Close()
	var out []domain.Alert
	for rows.Next() {
		var a domain.Alert
		var budget *string
		var thresholds []byte
		if err := rows.Scan(&a.ID, &a.CustomerID, &budget, &a.Currency, &thresholds, &a.WebhookURL, &a.MaxFired); err != nil {
			return nil, fmt.Errorf("postgres: scan alert: %w", err)
		}
		a.Budget = parseDec(budget)
		if len(thresholds) > 0 {
			_ = json.Unmarshal(thresholds, &a.Thresholds)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAlertFired(alertID string, maxFired int) error {
	_, err := s.pool.Exec(s.ctx, `UPDATE alerts SET max_fired = $2 WHERE id = $1`, alertID, maxFired)
	if err != nil {
		return fmt.Errorf("postgres: UpdateAlertFired: %w", err)
	}
	return nil
}

// --- wallet ---

func (s *Store) Wallet(customerID string) (domain.Wallet, bool, error) {
	var w domain.Wallet
	var bal *string
	err := s.pool.QueryRow(s.ctx,
		`SELECT id, customer_id, balance::text, currency FROM wallets WHERE customer_id = $1`, customerID,
	).Scan(&w.ID, &w.CustomerID, &bal, &w.Currency)
	if err == pgx.ErrNoRows {
		return domain.Wallet{}, false, nil
	}
	if err != nil {
		return domain.Wallet{}, false, fmt.Errorf("postgres: Wallet: %w", err)
	}
	w.Balance = parseDec(bal)
	return w, true, nil
}

// TopUpWallet credits the wallet atomically and idempotently. The unique
// constraint on wallet_transactions.idempotency_key makes a repeated top-up a
// no-op: the transaction insert is skipped and the balance is left untouched.
func (s *Store) TopUpWallet(customerID string, amount decimal.Decimal, currency, reason, idempotencyKey string) (domain.Wallet, error) {
	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("postgres: TopUpWallet begin: %w", err)
	}
	defer tx.Rollback(s.ctx)

	// Ensure a wallet row exists (create on first top-up).
	var walletID, walletCurrency string
	err = tx.QueryRow(s.ctx, `SELECT id, currency FROM wallets WHERE customer_id = $1`, customerID).Scan(&walletID, &walletCurrency)
	if err == pgx.ErrNoRows {
		walletID = id.New("wlt")
		walletCurrency = currency
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO wallets (id, customer_id, balance, currency) VALUES ($1,$2,0,$3)`,
			walletID, customerID, currency); err != nil {
			return domain.Wallet{}, fmt.Errorf("postgres: create wallet: %w", err)
		}
	} else if err != nil {
		return domain.Wallet{}, fmt.Errorf("postgres: load wallet: %w", err)
	}
	if walletCurrency != currency {
		return domain.Wallet{}, fmt.Errorf("postgres: wallet currency %s != top-up currency %s", walletCurrency, currency)
	}

	// Insert the transaction; ON CONFLICT means this key was already applied.
	key := idempotencyKey
	if key == "" {
		key = id.New("wtx") // unconditional credit if no key supplied
	}
	ct, err := tx.Exec(s.ctx,
		`INSERT INTO wallet_transactions (id, wallet_id, amount, reason, idempotency_key)
		 VALUES ($1,$2,$3::numeric,$4,$5) ON CONFLICT (idempotency_key) DO NOTHING`,
		id.New("wtx"), walletID, amount.String(), reason, key)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("postgres: insert wallet txn: %w", err)
	}
	if ct.RowsAffected() == 1 {
		// Newly applied — move the balance.
		if _, err := tx.Exec(s.ctx,
			`UPDATE wallets SET balance = balance + $2::numeric WHERE id = $1`, walletID, amount.String()); err != nil {
			return domain.Wallet{}, fmt.Errorf("postgres: update balance: %w", err)
		}
	}

	var bal *string
	if err := tx.QueryRow(s.ctx, `SELECT balance::text FROM wallets WHERE id = $1`, walletID).Scan(&bal); err != nil {
		return domain.Wallet{}, fmt.Errorf("postgres: read balance: %w", err)
	}
	if err := tx.Commit(s.ctx); err != nil {
		return domain.Wallet{}, fmt.Errorf("postgres: TopUpWallet commit: %w", err)
	}
	return domain.Wallet{ID: walletID, CustomerID: customerID, Balance: parseDec(bal), Currency: currency}, nil
}

func (s *Store) WalletTransactions(customerID string) ([]domain.WalletTransaction, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT wt.id, wt.wallet_id, wt.amount::text, wt.reason, wt.idempotency_key, wt.created_at
		 FROM wallet_transactions wt JOIN wallets w ON w.id = wt.wallet_id
		 WHERE w.customer_id = $1 ORDER BY wt.created_at DESC`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: WalletTransactions: %w", err)
	}
	defer rows.Close()
	var out []domain.WalletTransaction
	for rows.Next() {
		var t domain.WalletTransaction
		var amt *string
		if err := rows.Scan(&t.ID, &t.WalletID, &amt, &t.Reason, &t.IdempotencyKey, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan wallet txn: %w", err)
		}
		t.Amount = parseDec(amt)
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- dashboard reads ---

func (s *Store) ListCustomers() ([]domain.Customer, error) {
	rows, err := s.pool.Query(s.ctx, `SELECT id, external_id, name, created_at FROM customers ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: ListCustomers: %w", err)
	}
	defer rows.Close()
	var out []domain.Customer
	for rows.Next() {
		var c domain.Customer
		var ext *string
		if err := rows.Scan(&c.ID, &ext, &c.Name, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan customer: %w", err)
		}
		c.ExternalID = derefStr(ext)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) InvoicesForCustomer(customerID string) ([]domain.Invoice, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, customer_id, subscription_id, period_start, period_end, status, total::text, currency
		 FROM invoices WHERE customer_id = $1 ORDER BY period_start DESC`, customerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: InvoicesForCustomer: %w", err)
	}
	defer rows.Close()
	var out []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		var total *string
		if err := rows.Scan(&inv.ID, &inv.CustomerID, &inv.SubscriptionID, &inv.PeriodStart, &inv.PeriodEnd, &inv.Status, &total, &inv.Currency); err != nil {
			return nil, fmt.Errorf("postgres: scan invoice: %w", err)
		}
		inv.Total = parseDec(total)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// --- helpers ---

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func parseDec(s *string) decimal.Decimal {
	if s == nil || *s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
