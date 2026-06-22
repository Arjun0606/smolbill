// Package comms renders the customer-facing messages for failed-payment recovery:
// the branded, escalating email/SMS copy tied to each dunning state change. It is
// pure (template in, rendered text out) with no delivery of its own — smolbill
// hands the rendered subject+body to your channel (your ESP/SMS via the webhook
// payload), so YOU own and can preview the exact words. This inverts the
// merchant-of-record black box (Paddle: "we won't show you the dunning emails for
// compliance reasons") — here the templates are yours, editable, and previewable.
package comms

import (
	"bytes"
	"text/template"
)

// Event mirrors the dunning lifecycle transitions that warrant a message.
type Event string

const (
	PaymentFailed  Event = "payment_failed"  // a soft decline; a retry is scheduled
	RequiresAction Event = "requires_action" // a hard decline / SCA; the customer must act
	Recovered      Event = "recovered"       // a retry succeeded
	Uncollectible  Event = "uncollectible"   // recovery exhausted
)

// AllEvents lists the events that have a default template.
var AllEvents = []Event{PaymentFailed, RequiresAction, Recovered, Uncollectible}

// Template is the editable subject + body for one event. Placeholders are Go
// template fields over Context, e.g. {{.CustomerName}}, {{.Amount}}.
type Template struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Context is the data available to a template when it renders.
type Context struct {
	CustomerName string `json:"customer_name"`
	InvoiceID    string `json:"invoice_id"`
	Amount       string `json:"amount"`   // currency-formatted, e.g. "64.00"
	Currency     string `json:"currency"` // e.g. "USD"
	Attempts     int    `json:"attempts"`
	NextAttempt  string `json:"next_attempt"` // human date, or "" if none
	Reason       string `json:"reason"`       // decline reason, if any
	UpdateURL    string `json:"update_url"`   // where the customer updates their card
}

// Message is the rendered, ready-to-send result.
type Message struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// DefaultTemplates are the built-in, sensible starting copy for each event. They
// escalate: a calm first notice, a clear call to action on a hard decline, a
// thank-you on recovery, and a final notice when collection is exhausted.
var DefaultTemplates = map[Event]Template{
	PaymentFailed: {
		Subject: "We couldn't process your payment",
		Body: "Hi {{.CustomerName}},\n\n" +
			"We weren't able to process your payment of {{.Amount}} {{.Currency}}. " +
			"No action is needed yet — we'll automatically try again{{if .NextAttempt}} on {{.NextAttempt}}{{end}}.\n\n" +
			"If your card details have changed, you can update them here: {{.UpdateURL}}",
	},
	RequiresAction: {
		Subject: "Action needed: update your payment method",
		Body: "Hi {{.CustomerName}},\n\n" +
			"Your payment of {{.Amount}} {{.Currency}} was declined ({{.Reason}}) and we can't retry it automatically. " +
			"Please update your payment method to keep your account active:\n\n{{.UpdateURL}}",
	},
	Recovered: {
		Subject: "You're all set — payment received",
		Body: "Hi {{.CustomerName}},\n\n" +
			"Good news: your payment of {{.Amount}} {{.Currency}} went through. Thanks, and nothing more to do.",
	},
	Uncollectible: {
		Subject: "We were unable to collect your payment",
		Body: "Hi {{.CustomerName}},\n\n" +
			"After {{.Attempts}} attempts we were unable to collect {{.Amount}} {{.Currency}}. " +
			"To restore your account, please update your payment method: {{.UpdateURL}}",
	},
}

// For returns the template for an event, preferring an operator override and
// falling back to the built-in default.
func For(event Event, overrides map[Event]Template) Template {
	if t, ok := overrides[event]; ok && (t.Subject != "" || t.Body != "") {
		return t
	}
	return DefaultTemplates[event]
}

// Render fills a template with the context and returns the message. Missing
// fields render empty (missingkey=zero) rather than erroring, so a template that
// references an absent field degrades gracefully instead of failing delivery.
func Render(t Template, ctx Context) (Message, error) {
	subject, err := renderOne("subject", t.Subject, ctx)
	if err != nil {
		return Message{}, err
	}
	body, err := renderOne("body", t.Body, ctx)
	if err != nil {
		return Message{}, err
	}
	return Message{Subject: subject, Body: body}, nil
}

func renderOne(name, src string, ctx Context) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, ctx); err != nil {
		return "", err
	}
	return b.String(), nil
}
