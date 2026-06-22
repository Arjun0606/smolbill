// Package ingest is the front door for usage events. Its one job in v0 is the
// guarantee the silent challengers don't publish (build plan §3 #1, §6 #3):
// every event carries an idempotency_key, dedup happens on it within a
// documented, configurable window, and late / out-of-order events are accepted
// and attributed to their real event_time (not arrival time) so a re-rated
// period stays correct.
package ingest

import (
	"errors"
	"fmt"
	"time"

	"github.com/Arjun0606/smolbill/internal/domain"
)

// DefaultDedupWindow is the published idempotency window. An event whose
// idempotency_key was already seen is treated as a duplicate. We keep keys for
// this long; the value is documented so customers can reason about retries.
const DefaultDedupWindow = 720 * time.Hour // 30 days

// Store is the minimal persistence the ingester needs: remember which
// idempotency keys have been seen, and append accepted events.
type Store interface {
	// SeenKey reports whether an idempotency key was already accepted within the
	// dedup window ending at `now`.
	SeenKey(key string, now time.Time, window time.Duration) (bool, error)
	// AppendEvent durably stores an accepted event.
	AppendEvent(e domain.Event) error
}

// ErrDuplicate signals an event was a no-op duplicate (already ingested). It is
// not a failure: ingestion is idempotent, so callers should treat it as success
// and surface "already recorded" rather than erroring the customer's request.
var ErrDuplicate = errors.New("ingest: duplicate idempotency_key")

// Ingester accepts events with idempotent, late-tolerant semantics.
type Ingester struct {
	store  Store
	window time.Duration
}

// New builds an Ingester. A zero window falls back to DefaultDedupWindow.
func New(store Store, window time.Duration) *Ingester {
	if window <= 0 {
		window = DefaultDedupWindow
	}
	return &Ingester{store: store, window: window}
}

// Window returns the active dedup window (for surfacing in API docs / responses).
func (i *Ingester) Window() time.Duration { return i.window }

// Accept validates and records an event. `now` is the ingestion clock (injected
// for determinism/testing). Returns ErrDuplicate for a key already seen.
//
// Late and out-of-order events are first-class: we never reject an event for
// having an event_time in the past. Because aggregation keys off event_time and
// the invoice is recomputed from the event log (§6 #2), a late event simply
// re-rates its period correctly the next time the invoice is computed.
func (i *Ingester) Accept(e domain.Event, now time.Time) (domain.Event, error) {
	if e.IdempotencyKey == "" {
		return domain.Event{}, fmt.Errorf("ingest: idempotency_key is required")
	}
	if e.CustomerID == "" {
		return domain.Event{}, fmt.Errorf("ingest: customer_id is required")
	}
	if e.MeterCode == "" {
		return domain.Event{}, fmt.Errorf("ingest: meter_code is required")
	}
	if e.EventTime.IsZero() {
		return domain.Event{}, fmt.Errorf("ingest: event_time is required")
	}

	seen, err := i.store.SeenKey(e.IdempotencyKey, now, i.window)
	if err != nil {
		return domain.Event{}, err
	}
	if seen {
		return domain.Event{}, ErrDuplicate
	}

	e.IngestedAt = now
	if err := i.store.AppendEvent(e); err != nil {
		return domain.Event{}, err
	}
	return e, nil
}

// IsLate reports whether an event arrived after the close of the period it
// belongs to — useful for the re-rating / audit signal in the ledger. It does
// not affect acceptance; late events are always accepted.
func IsLate(e domain.Event, periodEnd time.Time) bool {
	return e.IngestedAt.After(periodEnd) && e.EventTime.Before(periodEnd)
}
