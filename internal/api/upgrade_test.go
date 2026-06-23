package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getBody(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

func TestPricingRendersBoundary(t *testing.T) {
	h := newHandle(t)

	code, body := getBody(t, h.ts.Config.Handler, "/pricing")
	if code != 200 {
		t.Fatalf("/pricing status = %d, want 200", code)
	}
	// The open-vs-Pro boundary must be visible, with the crown jewel on the free side.
	for _, want := range []string{"reconciliation", "Open core", "Pro", "commercial license", "AGPLv3"} {
		if !strings.Contains(body, want) {
			t.Errorf("/pricing missing %q", want)
		}
	}
	// /upgrade is an alias.
	if code2, _ := getBody(t, h.ts.Config.Handler, "/upgrade"); code2 != 200 {
		t.Fatalf("/upgrade status = %d, want 200", code2)
	}
}

func TestPricingCTAFlipsWithMode(t *testing.T) {
	h := newHandle(t)

	// Default (no env) = pre-launch waitlist.
	_, body := getBody(t, h.ts.Config.Handler, "/pricing")
	if !strings.Contains(body, "waitlist") {
		t.Error("default mode should show a waitlist CTA")
	}
	if !strings.Contains(body, "early access") {
		t.Error("waitlist mode should mention early access")
	}

	// Live mode = buy CTA label, no waitlist copy. The CTA routes through /checkout
	// (the destination URL itself is asserted in TestCheckoutRedirectsToCloudURL).
	t.Setenv("SMOLBILL_CLOUD_MODE", "live")
	t.Setenv("SMOLBILL_CLOUD_URL", "https://buy.smolbill.com/pro")
	_, body = getBody(t, h.ts.Config.Handler, "/pricing")
	if !strings.Contains(body, "Start on Cloud") {
		t.Error("live mode should show a buy CTA")
	}
	if !strings.Contains(body, `href="/checkout"`) {
		t.Error("the primary CTA should route through /checkout")
	}
	if strings.Contains(body, "early access") {
		t.Error("live mode should not show waitlist copy")
	}
}

func TestCheckoutRedirectsToCloudURL(t *testing.T) {
	h := newHandle(t)

	// Default (waitlist) destination.
	t.Setenv("SMOLBILL_CLOUD_URL", "https://list.smolbill.com")
	req := httptest.NewRequest(http.MethodGet, "/checkout", nil)
	rr := httptest.NewRecorder()
	h.ts.Config.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("/checkout status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "https://list.smolbill.com" {
		t.Fatalf("/checkout Location = %q, want the configured cloud URL", loc)
	}

	// Flip to live: same route now points at the checkout link.
	t.Setenv("SMOLBILL_CLOUD_MODE", "live")
	t.Setenv("SMOLBILL_CLOUD_URL", "https://buy.smolbill.com/pro")
	rr = httptest.NewRecorder()
	h.ts.Config.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/checkout", nil))
	if loc := rr.Header().Get("Location"); loc != "https://buy.smolbill.com/pro" {
		t.Fatalf("live /checkout Location = %q, want the buy URL", loc)
	}
}

func TestDashboardHasUpgradeLink(t *testing.T) {
	h := newHandle(t)
	_, body := getBody(t, h.ts.Config.Handler, "/dashboard")
	if !strings.Contains(body, "/pricing") {
		t.Error("dashboard should carry a tasteful link to /pricing")
	}
}
