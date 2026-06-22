package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Wallet is a customer's prepaid balance (build plan §8). It is given away in
// the OSS core — the embeddable customer wallet/portal is the feature Lago
// charges ~$1,500/mo for (§3 #6).
type Wallet struct {
	ID         string
	CustomerID string
	Balance    decimal.Decimal
	Currency   string
}

// WalletTransaction is an immutable, idempotent change to a wallet balance.
// Credits are positive, debits negative. Dedup is on IdempotencyKey.
type WalletTransaction struct {
	ID             string
	WalletID       string
	Amount         decimal.Decimal
	Reason         string
	IdempotencyKey string
	CreatedAt      time.Time
}
