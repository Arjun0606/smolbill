// Package id generates short, collision-resistant, prefixed identifiers
// (e.g. "evt_3f9a...", "inv_1c2d..."). Stripe-style readable prefixes make IDs
// self-describing in logs and the reconciliation ledger.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a new id with the given type prefix, e.g. New("evt").
func New(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return prefix + "_" + hex.EncodeToString(b)
}
