package ingest

import (
	"errors"
	"testing"
	"time"

	"github.com/Arjun0606/meterproof/internal/domain"
	"github.com/Arjun0606/meterproof/internal/store/memory"
)

func goodEvent(key string, eventTime time.Time) domain.Event {
	return domain.Event{
		IdempotencyKey: key,
		CustomerID:     "cus_1",
		MeterCode:      "tokens",
		EventTime:      eventTime,
		Properties:     map[string]any{"n": 100},
	}
}

func TestAcceptStoresEvent(t *testing.T) {
	st := memory.New()
	ing := New(st, 0)
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	got, err := ing.Accept(goodEvent("k1", now), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.IngestedAt != now {
		t.Fatalf("ingestedAt = %v, want %v", got.IngestedAt, now)
	}
	if n := len(st.EventsForCustomer("cus_1")); n != 1 {
		t.Fatalf("stored events = %d, want 1", n)
	}
}

func TestDuplicateKeyRejected(t *testing.T) {
	st := memory.New()
	ing := New(st, 0)
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	if _, err := ing.Accept(goodEvent("dup", now), now); err != nil {
		t.Fatal(err)
	}
	_, err := ing.Accept(goodEvent("dup", now.Add(time.Minute)), now.Add(time.Minute))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
	// The duplicate must not double-store.
	if n := len(st.EventsForCustomer("cus_1")); n != 1 {
		t.Fatalf("stored events = %d, want 1 (dup not stored)", n)
	}
}

func TestKeyForgottenOutsideWindow(t *testing.T) {
	st := memory.New()
	ing := New(st, time.Hour) // tiny window
	t0 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	if _, err := ing.Accept(goodEvent("k", t0), t0); err != nil {
		t.Fatal(err)
	}
	// Two hours later the key has aged out -> not a duplicate anymore.
	later := t0.Add(2 * time.Hour)
	if _, err := ing.Accept(goodEvent("k", later), later); err != nil {
		t.Fatalf("expected re-accept after window, got %v", err)
	}
}

func TestLateEventAccepted(t *testing.T) {
	st := memory.New()
	ing := New(st, 0)
	// event_time is in the past relative to ingestion -> still accepted.
	eventTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	got, err := ing.Accept(goodEvent("late", eventTime), now)
	if err != nil {
		t.Fatalf("late event rejected: %v", err)
	}
	if !got.EventTime.Equal(eventTime) {
		t.Fatal("late event must keep its real event_time, not arrival time")
	}
}

func TestIsLate(t *testing.T) {
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	e := domain.Event{
		EventTime:  time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), // belongs to June
		IngestedAt: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),  // arrived in July
	}
	if !IsLate(e, periodEnd) {
		t.Fatal("event ingested after period close should be flagged late")
	}
}

func TestValidationErrors(t *testing.T) {
	st := memory.New()
	ing := New(st, 0)
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	bad := map[string]domain.Event{
		"no key":      {CustomerID: "c", MeterCode: "m", EventTime: now},
		"no customer": {IdempotencyKey: "k", MeterCode: "m", EventTime: now},
		"no meter":    {IdempotencyKey: "k", CustomerID: "c", EventTime: now},
		"no time":     {IdempotencyKey: "k", CustomerID: "c", MeterCode: "m"},
	}
	for name, e := range bad {
		if _, err := ing.Accept(e, now); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
