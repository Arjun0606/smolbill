// Package dunning is the pure, deterministic core of failed-payment recovery:
// given an invoice's collection state, a retry schedule, and the outcome of a
// charge attempt, it decides what happens next — retry at time T, or give up as
// uncollectible. No I/O, no processor, no clock of its own (the caller passes
// `now`), so every transition is unit-testable and the cadence is fully under
// the operator's control — the exact opposite of a black-box "smart retry".
package dunning

import "time"

// Status is a collection's lifecycle state.
type Status string

const (
	// Scheduled: the invoice is unpaid and awaiting its first charge attempt.
	Scheduled Status = "scheduled"
	// Retrying: a soft decline; at least one retry remains on the schedule.
	Retrying Status = "retrying"
	// RequiresAction: a hard decline (expired/stolen card) or an
	// authentication_required result. Auto-retrying these can never succeed and
	// only burns attempts (and trips banks' fraud-velocity limits), so we STOP and
	// wait for the customer to fix their card or authenticate. Not terminal — once
	// they act, collection can resume — but never auto-retried.
	RequiresAction Status = "requires_action"
	// Paid: collected successfully (terminal).
	Paid Status = "paid"
	// Uncollectible: every scheduled retry failed (terminal).
	Uncollectible Status = "uncollectible"
)

// FailureClass categorizes a processor decline to decide whether a retry can ever
// succeed. Routing by reason — instead of blind-retrying everything like Stripe's
// Smart Retries — is the difference between recovering revenue and looking like
// fraud to a customer's bank.
type FailureClass string

const (
	SoftDecline  FailureClass = "soft"          // transient: retry on the schedule
	HardDecline  FailureClass = "hard"          // card unusable: stop, request a new card
	AuthRequired FailureClass = "auth_required" // SCA/3DS: stop, customer must authenticate
)

// hardDeclines are processor reason codes where the card itself is unusable, so
// no number of retries will help — only a new payment method will.
var hardDeclines = map[string]bool{
	"expired_card":                true,
	"incorrect_number":            true,
	"invalid_number":              true,
	"invalid_account":             true,
	"stolen_card":                 true,
	"lost_card":                   true,
	"pickup_card":                 true,
	"no_such_card":                true,
	"card_not_supported":          true,
	"revocation_of_authorization": true,
}

// Classify maps a processor decline reason to a FailureClass. Unknown reasons are
// treated as soft (retry) — better to give a charge another chance than to write
// off revenue on an ambiguous code.
func Classify(reason string) FailureClass {
	switch {
	case reason == "authentication_required":
		return AuthRequired
	case hardDeclines[reason]:
		return HardDecline
	default:
		return SoftDecline
	}
}

// Schedule is the set of delays — measured from the FIRST failure — at which
// successive retries are attempted. There are len(Schedule) retries after the
// initial attempt; once the final one fails the invoice is uncollectible. The
// cadence is explicit data, not hidden heuristics, so an operator can see and
// change exactly when customers are charged.
type Schedule []time.Duration

// DefaultSchedule is grounded in Recurly's published analysis of 40M failed
// transactions: a fast first retry (~2h) catches transient bank/network glitches
// and recovers ~22% on its own; a Day-1/3/5/7 spread thereafter recovers ~58%
// with no customer contact; ~90% of all recoveries land inside the first 10 days.
// So: retry at +2h, +1d, +3d, +5d, +7d after the first failure (six attempts,
// front-loaded), then stop. Every delay is explicit and overridable — the
// operator sees and controls the cadence, unlike a black-box "smart retry".
var DefaultSchedule = Schedule{
	2 * time.Hour,
	24 * time.Hour,
	72 * time.Hour,
	120 * time.Hour,
	168 * time.Hour,
}

// State is the dunning state of one invoice. It is a value type; transitions
// return a new State rather than mutating in place.
type State struct {
	Status        Status
	Attempts      int       // total charge attempts made so far
	FirstFailedAt time.Time // zero until the first failure
	NextAttemptAt time.Time // when the next retry is due (zero in terminal states)
	LastReason    string    // processor's reason for the most recent failure
}

// New returns the initial state for a freshly finalized, unpaid invoice.
func New() State { return State{Status: Scheduled} }

// Terminal reports whether a status is final (paid or written off) — no further
// attempts will ever be made.
func (st Status) Terminal() bool { return st == Paid || st == Uncollectible }

// Terminal reports whether the collection is finished (paid or written off).
func (s State) Terminal() bool { return s.Status.Terminal() }

// Due reports whether a charge should be attempted now: the first attempt for a
// freshly scheduled invoice, or a retry whose delay has elapsed.
func (s State) Due(now time.Time) bool {
	switch s.Status {
	case Scheduled:
		return true
	case Retrying:
		return !s.NextAttemptAt.After(now)
	default:
		return false
	}
}

// RecordAttempt folds the outcome of a charge attempt into the state and returns
// the next state. On success the invoice is Paid. On failure it schedules the
// next retry from the FIRST failure time per the schedule, or — once the schedule
// is exhausted — marks the invoice Uncollectible. `now` is the attempt time.
func (s State) RecordAttempt(success bool, reason string, sched Schedule, now time.Time) State {
	s.Attempts++
	if success {
		s.Status = Paid
		s.NextAttemptAt = time.Time{}
		s.LastReason = ""
		return s
	}
	s.LastReason = reason
	if s.FirstFailedAt.IsZero() {
		s.FirstFailedAt = now
	}
	// Route by decline reason. A hard decline (dead card) or an SCA challenge can
	// never be fixed by retrying — only the customer can act — so stop now rather
	// than burn the remaining attempts and trip the bank's fraud-velocity limits.
	if Classify(reason) != SoftDecline {
		s.Status = RequiresAction
		s.NextAttemptAt = time.Time{}
		return s
	}
	// Soft decline: schedule the next retry from the FIRST failure per the
	// schedule. After the first failure (Attempts == 1) we consult sched[0]; after
	// the second, sched[1]; and so on. Once exhausted, the invoice is written off.
	idx := s.Attempts - 1
	if idx >= len(sched) {
		s.Status = Uncollectible
		s.NextAttemptAt = time.Time{}
		return s
	}
	s.Status = Retrying
	s.NextAttemptAt = s.FirstFailedAt.Add(sched[idx])
	return s
}
