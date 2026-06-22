package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Wallet is a customer's prepaid balance (build plan §8). It is given away in
// the OSS core — the embeddable customer wallet/portal is the feature Lago
// charges ~$1,500/mo for (§3 #6).
type Wallet struct {
	ID         string          `json:"id"`
	CustomerID string          `json:"customer_id"`
	Balance    decimal.Decimal `json:"balance"`
	Currency   string          `json:"currency"`
}

// WalletTransaction is an immutable, idempotent change to a wallet balance.
// Credits are positive, debits negative. Dedup is on IdempotencyKey.
type WalletTransaction struct {
	ID             string          `json:"id"`
	WalletID       string          `json:"wallet_id"`
	Amount         decimal.Decimal `json:"amount"`
	Reason         string          `json:"reason"`
	IdempotencyKey string          `json:"idempotency_key"`
	CreatedAt      time.Time       `json:"created_at"`
}
