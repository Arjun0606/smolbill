package api

import (
	"net/http"
	"os"
	"strings"
)

// The upgrade funnel: a single honest conversion surface (/pricing) plus tasteful
// "this is a Pro/Cloud feature" awareness — never naggy popups (OSS users, and HN,
// hate nagware). Everything points at one destination whose call-to-action flips
// between a pre-launch WAITLIST and a live BUY button via env, which is exactly the
// controlled-launch sequence: pool a warm audience first, then flip to buy at the
// blow-up moment without shipping new code.
//
// Env:
//
//	SMOLBILL_CLOUD_MODE   "waitlist" (default) | "live"
//	SMOLBILL_CLOUD_URL    CTA destination (waitlist signup, or the Dodo checkout)
//	SMOLBILL_COMMERCIAL_URL  commercial-license CTA destination
type cloudConfig struct {
	Live          bool   // true => buy button; false => waitlist
	CTALabel      string // primary CTA text, derived from mode
	CloudURL      string // where the primary CTA points
	CommercialURL string // commercial-license CTA
}

func cloudConfigFromEnv() cloudConfig {
	live := strings.EqualFold(strings.TrimSpace(os.Getenv("SMOLBILL_CLOUD_MODE")), "live")
	cloudURL := envOr("SMOLBILL_CLOUD_URL", "https://smolbill.com/pricing")
	label := "Join the Cloud waitlist"
	if live {
		label = "Start on Cloud"
	}
	return cloudConfig{
		Live:          live,
		CTALabel:      label,
		CloudURL:      cloudURL,
		CommercialURL: envOr("SMOLBILL_COMMERCIAL_URL", "https://smolbill.com/pricing"),
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// proFeature is one row of the open-vs-Pro boundary, mirroring README/BUSINESS.md.
type proFeature struct {
	Name string
	Open bool // true => in the free AGPL core; false => Pro/Cloud (closed)
	Note string
}

var proBoundary = []proFeature{
	{"Deterministic engine + provable reconciliation", true, "the crown jewel — free forever"},
	{"MCP (stdio + HTTP) + REST + SDKs", true, ""},
	{"All payment rails (Stripe, Dodo, Paddle, Lemon Squeezy, Polar, Creem, Razorpay, crypto)", true, ""},
	{"Dashboard, customer portal, transparent dunning logic", true, ""},
	{"Revenue analytics", false, "cross-customer insight"},
	{"SSO / RBAC, audit-log retention", false, "enterprise controls"},
	{"Cross-merchant ML retry timing", false, "needs many merchants' data — can't be self-hosted"},
	{"Managed card-updater / network tokens", false, "gated by card-network enrollment"},
	{"Managed hosting, hosted MCP at scale, SLA", false, "we run it for you"},
}

// pricing renders the conversion hub: the open-vs-Pro boundary and a mode-aware CTA.
func (s *Server) pricing(w http.ResponseWriter, _ *http.Request) {
	s.renderHTML(w, "pricing.html", map[string]any{
		"Cloud":    cloudConfigFromEnv(),
		"Features": proBoundary,
	})
}

// checkout is the buy button as a real, stable app route (so clicks are trackable
// and the destination is swappable without touching the page): it 302-redirects to
// the current cloud destination — the waitlist signup pre-launch, or the live
// checkout link once SMOLBILL_CLOUD_MODE=live. One env flip flips the funnel.
func (s *Server) checkout(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, cloudConfigFromEnv().CloudURL, http.StatusFound)
}
