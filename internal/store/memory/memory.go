// Package memory is an in-memory implementation of store.Store, sufficient to
// exercise the full deterministic pipeline (ingest -> aggregate -> price ->
// invoice) without Postgres. It backs the CLI and tests; the Postgres backend
// satisfies the same interface. Safe for concurrent use.
package memory

import (
	"sync"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/id"
)

// Store holds all engine state in memory.
type Store struct {
	mu           sync.RWMutex
	customers    map[string]domain.Customer
	meters       map[string]domain.Meter // keyed by meter code
	plans        map[string]domain.Plan  // keyed by id
	subs         map[string]domain.Subscription
	events       []domain.Event
	seenKeys     map[string]time.Time            // idempotency_key -> ingestedAt
	invoices     map[string]domain.Invoice       // keyed by id
	ledger       map[string][]domain.LedgerRow   // invoice_id -> rows
	entitlements map[string][]domain.Entitlement // customer_id -> entitlements
	alerts       map[string]domain.Alert         // keyed by id
}

// New returns an empty store.
func New() *Store {
	return &Store{
		customers:    map[string]domain.Customer{},
		meters:       map[string]domain.Meter{},
		plans:        map[string]domain.Plan{},
		subs:         map[string]domain.Subscription{},
		seenKeys:     map[string]time.Time{},
		invoices:     map[string]domain.Invoice{},
		ledger:       map[string][]domain.LedgerRow{},
		entitlements: map[string][]domain.Entitlement{},
		alerts:       map[string]domain.Alert{},
	}
}

// --- ingest ---

func (s *Store) SeenKey(key string, now time.Time, window time.Duration) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seenAt, ok := s.seenKeys[key]
	if !ok {
		return false, nil
	}
	return !seenAt.Before(now.Add(-window)), nil
}

func (s *Store) AppendEvent(e domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	s.seenKeys[e.IdempotencyKey] = e.IngestedAt
	return nil
}

// --- entities ---

func (s *Store) PutCustomer(c domain.Customer) error { return s.put(func() { s.customers[c.ID] = c }) }
func (s *Store) PutMeter(m domain.Meter) error       { return s.put(func() { s.meters[m.Code] = m }) }
func (s *Store) PutPlan(p domain.Plan) error         { return s.put(func() { s.plans[p.ID] = p }) }
func (s *Store) PutSubscription(sub domain.Subscription) error {
	return s.put(func() { s.subs[sub.ID] = sub })
}

func (s *Store) put(fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	return nil
}

func (s *Store) GetCustomer(id string) (domain.Customer, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.customers[id]
	return c, ok, nil
}

func (s *Store) GetPlan(id string) (domain.Plan, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	return p, ok, nil
}

func (s *Store) GetSubscription(id string) (domain.Subscription, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subs[id]
	return sub, ok, nil
}

func (s *Store) SubscriptionsForCustomer(customerID string) ([]domain.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Subscription
	for _, sub := range s.subs {
		if sub.CustomerID == customerID {
			out = append(out, sub)
		}
	}
	return out, nil
}

func (s *Store) Meters() (map[string]domain.Meter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]domain.Meter, len(s.meters))
	for k, v := range s.meters {
		out[k] = v
	}
	return out, nil
}

func (s *Store) EventsForCustomer(customerID string) ([]domain.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Event
	for _, e := range s.events {
		if e.CustomerID == customerID {
			out = append(out, e)
		}
	}
	return out, nil
}

// --- invoices + ledger ---

func (s *Store) SaveFinalizedInvoice(inv domain.Invoice, ledger []domain.LedgerRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invoices[inv.ID] = inv
	rows := make([]domain.LedgerRow, len(ledger))
	for i, r := range ledger {
		if r.ID == "" {
			r.ID = id.New("ldg")
		}
		r.InvoiceID = inv.ID
		rows[i] = r
	}
	s.ledger[inv.ID] = rows
	return nil
}

func (s *Store) GetInvoice(invID string) (domain.Invoice, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.invoices[invID]
	return inv, ok, nil
}

func (s *Store) GetLedger(invoiceID string) ([]domain.LedgerRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.LedgerRow(nil), s.ledger[invoiceID]...), nil
}

// --- entitlements ---

func (s *Store) PutEntitlement(e domain.Entitlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.entitlements[e.CustomerID]
	for i := range list {
		if list[i].ID == e.ID || (list[i].Feature == e.Feature && e.ID == "") {
			list[i] = e
			s.entitlements[e.CustomerID] = list
			return nil
		}
	}
	s.entitlements[e.CustomerID] = append(list, e)
	return nil
}

func (s *Store) EntitlementsForCustomer(customerID string) ([]domain.Entitlement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.Entitlement(nil), s.entitlements[customerID]...), nil
}

// --- alerts ---

func (s *Store) PutAlert(a domain.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = id.New("alert")
	}
	s.alerts[a.ID] = a
	return nil
}

func (s *Store) AlertsForCustomer(customerID string) ([]domain.Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Alert
	for _, a := range s.alerts {
		if a.CustomerID == customerID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Store) UpdateAlertFired(alertID string, maxFired int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.alerts[alertID]
	if !ok {
		return nil
	}
	a.MaxFired = maxFired
	s.alerts[alertID] = a
	return nil
}
