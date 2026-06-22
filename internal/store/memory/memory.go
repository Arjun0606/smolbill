// Package memory is an in-memory implementation of store.Store, sufficient to
// exercise the full deterministic pipeline (ingest -> aggregate -> price ->
// invoice) without Postgres. It backs the CLI and tests; the Postgres backend
// satisfies the same interface. Safe for concurrent use.
package memory

import (
	"sync"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// Store holds all engine state in memory.
type Store struct {
	mu        sync.RWMutex
	customers map[string]domain.Customer
	meters    map[string]domain.Meter // keyed by meter code
	plans     map[string]domain.Plan  // keyed by id
	subs      map[string]domain.Subscription
	events    []domain.Event
	seenKeys  map[string]time.Time // idempotency_key -> ingestedAt
}

// New returns an empty store.
func New() *Store {
	return &Store{
		customers: map[string]domain.Customer{},
		meters:    map[string]domain.Meter{},
		plans:     map[string]domain.Plan{},
		subs:      map[string]domain.Subscription{},
		seenKeys:  map[string]time.Time{},
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
