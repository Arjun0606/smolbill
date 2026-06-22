package comms

import (
	"strings"
	"testing"
)

func TestRenderFillsContext(t *testing.T) {
	ctx := Context{
		CustomerName: "Acme", Amount: "64.00", Currency: "USD",
		NextAttempt: "2026-06-25", UpdateURL: "https://pay.test/update", Attempts: 3, Reason: "expired_card",
	}
	m, err := Render(DefaultTemplates[PaymentFailed], ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Body, "Acme") || !strings.Contains(m.Body, "64.00 USD") {
		t.Errorf("payment_failed body missing context: %q", m.Body)
	}
	if !strings.Contains(m.Body, "2026-06-25") {
		t.Errorf("expected the next-attempt date in the body: %q", m.Body)
	}
}

func TestPaymentFailedOmitsNextAttemptWhenAbsent(t *testing.T) {
	// The {{if .NextAttempt}} guard must drop the clause cleanly when there's no date.
	m, err := Render(DefaultTemplates[PaymentFailed], Context{CustomerName: "Acme", Amount: "10.00", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Body, "on ") && strings.Contains(m.Body, "try again on ") {
		t.Errorf("empty next-attempt should not leave a dangling 'on': %q", m.Body)
	}
}

func TestForPrefersOverride(t *testing.T) {
	overrides := map[Event]Template{Recovered: {Subject: "Custom thanks", Body: "ty {{.CustomerName}}"}}
	if got := For(Recovered, overrides); got.Subject != "Custom thanks" {
		t.Errorf("override not used: %+v", got)
	}
	// An empty override falls back to the default.
	if got := For(Recovered, map[Event]Template{Recovered: {}}); got.Subject != DefaultTemplates[Recovered].Subject {
		t.Errorf("empty override should fall back to default, got %+v", got)
	}
	// No override -> default.
	if got := For(PaymentFailed, nil); got.Subject != DefaultTemplates[PaymentFailed].Subject {
		t.Errorf("missing override should use default, got %+v", got)
	}
}

func TestRenderRejectsMalformedTemplate(t *testing.T) {
	if _, err := Render(Template{Subject: "ok", Body: "{{.Unclosed"}, Context{}); err == nil {
		t.Error("expected a parse error for a malformed template body")
	}
	if _, err := Render(Template{Subject: "{{.Bad", Body: "ok"}, Context{}); err == nil {
		t.Error("expected a parse error for a malformed subject")
	}
}

func TestEveryEventHasADefault(t *testing.T) {
	for _, e := range AllEvents {
		tmpl := DefaultTemplates[e]
		if tmpl.Subject == "" || tmpl.Body == "" {
			t.Errorf("event %q has an incomplete default template", e)
		}
		if _, err := Render(tmpl, Context{}); err != nil {
			t.Errorf("default template %q fails to render: %v", e, err)
		}
	}
}
