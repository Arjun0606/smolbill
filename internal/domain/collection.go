package domain

import "time"

// Collection is the persisted dunning state of one finalized, unpaid invoice: the
// recovery record that tracks every charge attempt, why it failed, and when the
// next retry is due. It is the durable mirror of the pure dunning.State machine.
//
// This is the thing Lago gates behind a premium license (and you must email
// sales to get) and Chargebee behind a $250/mo add-on — given away here in the
// Apache-2.0 core, fully inspectable: the cadence is data, not a black box.
type Collection struct {
	InvoiceID     string     `json:"invoice_id"`
	ExternalID    string     `json:"external_invoice_id"` // processor invoice the charge is attempted against
	Status        string     `json:"status"`              // scheduled|retrying|requires_action|paid|uncollectible
	Attempts      int        `json:"attempts"`
	FirstFailedAt *time.Time `json:"first_failed_at"` // null until the first failure
	NextAttemptAt *time.Time `json:"next_attempt_at"` // null in terminal / requires_action states
	LastReason    string     `json:"last_reason"`     // processor's reason for the most recent failure
	Currency      string     `json:"currency"`
	AmountMinor   int64      `json:"amount_minor"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
