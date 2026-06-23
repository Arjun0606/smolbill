package api

import "testing"

func TestRateLimiterBurstThenBlocks(t *testing.T) {
	// burst of 3, very low refill: first 3 pass, the 4th is blocked.
	rl := newRateLimiter(0.0001, 3)
	for i := 0; i < 3; i++ {
		if !rl.allow("k") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	if rl.allow("k") {
		t.Fatal("4th request should be rate-limited")
	}
}

func TestRateLimiterPerKeyIsolated(t *testing.T) {
	rl := newRateLimiter(0.0001, 1)
	if !rl.allow("a") || !rl.allow("b") {
		t.Fatal("different keys have independent buckets")
	}
	if rl.allow("a") {
		t.Fatal("key a should be exhausted after its one token")
	}
}
