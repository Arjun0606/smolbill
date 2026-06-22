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

// --- helpers ---

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
