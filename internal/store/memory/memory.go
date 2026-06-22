// Package memory is an in-memory implementation of the engine's storage,
// sufficient to exercise the full deterministic pipeline (ingest -> aggregate ->
// price -> invoice) without Postgres. It backs the CLI and the tests; the
// Postgres implementation (Phase 1 finish / Phase 2) satisfies the same shape.
//
// It is safe for concurrent use so the CLI/HTTP layer can share one instance.
package memory

import (
	"sync"
	"time"

	"github.com/Arjun0606/meterproof/internal/domain"
)

// Store holds all engine state in memory.
type Store struct {
	mu         sync.RWMutex
	customers  map[string]domain.Customer
	meters     map[string]domain.Meter // keyed by meter code
	plans      map[string]domain.Plan  // keyed by id (one version per id here)
	subs       map[string]domain.Subscription
	events     []domain.Event
	seenKeys   map[string]time.Time // idempotency_key -> ingestedAt
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

// --- ingest.Store ---

// SeenKey reports whether the idempotency key was accepted within [now-window, now].
func (s *Store) SeenKey(key string, now time.Time, window time.Duration) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seenAt, ok := s.seenKeys[key]
	if !ok {
		return false, nil
	}
	// Outside the window the key is forgotten (treated as not seen).
	return !seenAt.Before(now.Add(-window)), nil
}

// AppendEvent stores an accepted event and records its idempotency key.
func (s *Store) AppendEvent(e domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	s.seenKeys[e.IdempotencyKey] = e.IngestedAt
	return nil
}

// --- entity accessors ---

func (s *Store) PutCustomer(c domain.Customer) { s.put(func() { s.customers[c.ID] = c }) }
func (s *Store) PutMeter(m domain.Meter)       { s.put(func() { s.meters[m.Code] = m }) }
func (s *Store) PutPlan(p domain.Plan)         { s.put(func() { s.plans[p.ID] = p }) }
func (s *Store) PutSubscription(sub domain.Subscription) {
	s.put(func() { s.subs[sub.ID] = sub })
}

func (s *Store) put(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// Meters returns a copy of the meter map keyed by code (for invoice calc).
func (s *Store) Meters() map[string]domain.Meter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]domain.Meter, len(s.meters))
	for k, v := range s.meters {
		out[k] = v
	}
	return out
}

// Plan looks up a plan by id.
func (s *Store) Plan(id string) (domain.Plan, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	return p, ok
}

// Subscription looks up a subscription by id.
func (s *Store) Subscription(id string) (domain.Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subs[id]
	return sub, ok
}

// EventsForCustomer returns all stored events for a customer (copied).
func (s *Store) EventsForCustomer(customerID string) []domain.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Event
	for _, e := range s.events {
		if e.CustomerID == customerID {
			out = append(out, e)
		}
	}
	return out
}
