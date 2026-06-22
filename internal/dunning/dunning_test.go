package dunning

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// sched is a small fixed schedule for deterministic assertions: retry at +1d, +2d.
var sched = Schedule{24 * time.Hour, 48 * time.Hour}

func TestFirstAttemptSucceeds(t *testing.T) {
	s := New().RecordAttempt(true, "", sched, t0)
	if s.Status != Paid {
		t.Fatalf("status = %s, want paid", s.Status)
	}
	if s.Attempts != 1 || !s.Terminal() {
		t.Fatalf("unexpected state: %+v", s)
	}
	if !s.NextAttemptAt.IsZero() {
		t.Error("paid state must have no next attempt")
	}
}

func TestRecoversAfterFailures(t *testing.T) {
	s := New()
	if !s.Due(t0) {
		t.Fatal("a freshly scheduled invoice must be due immediately")
	}

	// Attempt 1 fails -> retry scheduled at firstFailed (+sched[0] = +1d).
	s = s.RecordAttempt(false, "card_declined", sched, t0)
	if s.Status != Retrying || s.Attempts != 1 {
		t.Fatalf("after 1st failure: %+v", s)
	}
	if !s.NextAttemptAt.Equal(t0.Add(24 * time.Hour)) {
		t.Errorf("next attempt = %v, want +24h", s.NextAttemptAt)
	}
	if s.LastReason != "card_declined" {
		t.Errorf("reason = %q", s.LastReason)
	}
	// Not due before the delay elapses; due at/after it.
	if s.Due(t0.Add(23 * time.Hour)) {
		t.Error("must not be due before the retry delay")
	}
	if !s.Due(t0.Add(24 * time.Hour)) {
		t.Error("must be due once the retry delay elapses")
	}

	// Attempt 2 (at +1d) fails -> next retry measured from FIRST failure (+2d).
	s = s.RecordAttempt(false, "insufficient_funds", sched, t0.Add(24*time.Hour))
	if s.Status != Retrying || s.Attempts != 2 {
		t.Fatalf("after 2nd failure: %+v", s)
	}
	if !s.NextAttemptAt.Equal(t0.Add(48 * time.Hour)) {
		t.Errorf("next attempt = %v, want firstFailed+48h", s.NextAttemptAt)
	}

	// Attempt 3 (at +2d) succeeds -> recovered.
	s = s.RecordAttempt(true, "", sched, t0.Add(48*time.Hour))
	if s.Status != Paid || s.Attempts != 3 {
		t.Fatalf("after recovery: %+v", s)
	}
}

func TestExhaustsToUncollectible(t *testing.T) {
	s := New()
	// 1 initial + len(sched) retries = 3 attempts, all failing.
	s = s.RecordAttempt(false, "x", sched, t0)                   // attempt 1 -> retrying
	s = s.RecordAttempt(false, "x", sched, t0.Add(24*time.Hour)) // attempt 2 -> retrying
	if s.Status != Retrying {
		t.Fatalf("before exhaustion: %+v", s)
	}
	s = s.RecordAttempt(false, "x", sched, t0.Add(48*time.Hour)) // attempt 3 -> exhausted
	if s.Status != Uncollectible || s.Attempts != 3 {
		t.Fatalf("want uncollectible after %d attempts, got %+v", len(sched)+1, s)
	}
	if !s.Terminal() || s.Due(t0.Add(1000*time.Hour)) {
		t.Error("uncollectible must be terminal and never due")
	}
	if !s.NextAttemptAt.IsZero() {
		t.Error("uncollectible must have no next attempt")
	}
}

func TestFirstFailedAtPinnedToFirstFailure(t *testing.T) {
	s := New().RecordAttempt(false, "a", sched, t0)
	first := s.FirstFailedAt
	if !first.Equal(t0) {
		t.Fatalf("first_failed_at = %v, want %v", first, t0)
	}
	// A later failure must NOT move first_failed_at — retries are spaced from the
	// original failure, not the most recent one.
	s = s.RecordAttempt(false, "b", sched, t0.Add(24*time.Hour))
	if !s.FirstFailedAt.Equal(first) {
		t.Errorf("first_failed_at moved to %v; must stay %v", s.FirstFailedAt, first)
	}
}

func TestHardDeclineStopsRetrying(t *testing.T) {
	// An expired card can't be fixed by retrying — go straight to requires_action,
	// not retrying, and schedule nothing.
	s := New().RecordAttempt(false, "expired_card", sched, t0)
	if s.Status != RequiresAction {
		t.Fatalf("status = %s, want requires_action", s.Status)
	}
	if !s.NextAttemptAt.IsZero() {
		t.Error("a hard decline must not schedule a retry")
	}
	if s.Due(t0.Add(1000 * time.Hour)) {
		t.Error("requires_action must never be auto-due")
	}
	if s.Terminal() {
		t.Error("requires_action is not terminal — the customer can still fix it")
	}
}

func TestAuthRequiredStopsRetrying(t *testing.T) {
	s := New().RecordAttempt(false, "authentication_required", sched, t0)
	if s.Status != RequiresAction {
		t.Fatalf("status = %s, want requires_action", s.Status)
	}
	if !s.NextAttemptAt.IsZero() {
		t.Error("an SCA challenge must not schedule a blind retry")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]FailureClass{
		"insufficient_funds":      SoftDecline,
		"processing_error":        SoftDecline,
		"card_declined":           SoftDecline, // generic/ambiguous -> give it another chance
		"":                        SoftDecline, // unknown -> soft
		"expired_card":            HardDecline,
		"stolen_card":             HardDecline,
		"lost_card":               HardDecline,
		"authentication_required": AuthRequired,
	}
	for reason, want := range cases {
		if got := Classify(reason); got != want {
			t.Errorf("Classify(%q) = %s, want %s", reason, got, want)
		}
	}
}

func TestDefaultScheduleShape(t *testing.T) {
	if len(DefaultSchedule) != 5 {
		t.Fatalf("default schedule has %d retries, want 5", len(DefaultSchedule))
	}
	// The first retry should be fast (<= ~6h) to catch transient declines.
	if DefaultSchedule[0] > 6*time.Hour {
		t.Errorf("first retry at %v; research says fire fast (~2h)", DefaultSchedule[0])
	}
	// Must be strictly increasing (spread out), all positive.
	for i, d := range DefaultSchedule {
		if d <= 0 {
			t.Errorf("delay %d is non-positive: %v", i, d)
		}
		if i > 0 && d <= DefaultSchedule[i-1] {
			t.Errorf("schedule not strictly increasing at %d: %v <= %v", i, d, DefaultSchedule[i-1])
		}
	}
}
