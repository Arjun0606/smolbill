package providers

import (
	"os"
	"testing"
)

// clearEnv unsets every rail credential so a test starts from a known-empty state.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SMOLBILL_PROCESSOR",
		"STRIPE_SECRET_KEY",
		"DODO_PAYMENTS_API_KEY", "DODO_PAYMENTS_LIVE",
		"PADDLE_API_KEY", "PADDLE_SANDBOX",
		"LEMONSQUEEZY_API_KEY", "LEMONSQUEEZY_STORE_ID",
		"RAZORPAY_KEY_ID", "RAZORPAY_KEY_SECRET",
		"CRYPTO_PAY_API_KEY", "CRYPTO_PAY_BASE_URL",
	} {
		t.Setenv(k, "") // registers cleanup to restore
		_ = os.Unsetenv(k)
	}
}

func TestSelectNoneConfigured(t *testing.T) {
	clearEnv(t)
	p, err := Select()
	if err != nil || p != nil {
		t.Fatalf("want (nil,nil) when nothing configured, got p=%v err=%v", p, err)
	}
}

func TestAutoDetectPrefersStripeThenOrder(t *testing.T) {
	clearEnv(t)
	t.Setenv("DODO_PAYMENTS_API_KEY", "dodo_x")
	p, err := Select()
	if err != nil || p == nil || p.Name() != "dodo" {
		t.Fatalf("want dodo auto-detected, got p=%v err=%v", p, err)
	}
	// Stripe present too -> Stripe wins on registry order.
	t.Setenv("STRIPE_SECRET_KEY", "sk_x")
	p, err = Select()
	if err != nil || p == nil || p.Name() != "stripe" {
		t.Fatalf("want stripe to win auto-detect order, got p=%v err=%v", p, err)
	}
}

func TestExplicitSelectionRespected(t *testing.T) {
	clearEnv(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_x")
	t.Setenv("RAZORPAY_KEY_ID", "rzp_id")
	t.Setenv("RAZORPAY_KEY_SECRET", "rzp_secret")
	t.Setenv("SMOLBILL_PROCESSOR", "razorpay")
	p, err := Select()
	if err != nil || p == nil || p.Name() != "razorpay" {
		t.Fatalf("explicit razorpay should win over stripe, got p=%v err=%v", p, err)
	}
}

func TestExplicitButUnconfiguredErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("SMOLBILL_PROCESSOR", "paddle") // no PADDLE_API_KEY
	if _, err := Select(); err == nil {
		t.Fatal("want error when the explicitly-selected rail has no credentials")
	}
}

func TestUnknownProcessorErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("SMOLBILL_PROCESSOR", "venmo")
	if _, err := Select(); err == nil {
		t.Fatal("want error for an unknown SMOLBILL_PROCESSOR")
	}
}
