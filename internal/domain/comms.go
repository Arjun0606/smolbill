package domain

import "time"

// MessageTemplate is an operator's override of the customer-facing copy for a
// dunning event (payment_failed, requires_action, recovered, uncollectible).
// When none is stored, smolbill uses a sensible built-in default. This is the
// "you own and can edit the dunning emails" capability — the opposite of a
// merchant-of-record that hides them from you.
type MessageTemplate struct {
	Event     string    `json:"event"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}
