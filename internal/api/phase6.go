package api

import (
	"net/http"

	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/id"
)

// --- POST /v1/webhooks, GET /v1/webhooks ---
//
// Register an HTTP endpoint to receive lifecycle events. smolbill POSTs a signed
// JSON payload (X-Smolbill-Signature: HMAC-SHA256 of the body under the secret)
// whenever a subscribed event fires:
//
//   invoice.finalized — an invoice was materialized + persisted
//   drift.detected    — a reconciliation found the bill no longer agrees with the
//                       live event log (internal) or with the payment processor
//
// An empty `events` list subscribes to everything. The signing secret is returned
// once, on creation — store it; it is never shown again.

type webhookReq struct {
	URL    string   `json:"url"`
	Events []string `json:"events"` // omit/empty = all events
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var req webhookReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		writeErr(w, http.StatusBadRequest, "url is required")
		return
	}
	wh := domain.Webhook{
		ID:        id.New("wh"),
		URL:       req.URL,
		Events:    req.Events,
		Secret:    id.New("whsec"),
		CreatedAt: s.now(),
	}
	if err := s.store.PutWebhook(wh); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The secret is returned exactly once, here — like Stripe's whsec_. Use it to
	// verify X-Smolbill-Signature on every delivery.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         wh.ID,
		"url":        wh.URL,
		"events":     events(wh.Events),
		"secret":     wh.Secret,
		"created_at": wh.CreatedAt,
	})
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.store.Webhooks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(hooks))
	for _, h := range hooks {
		// Secrets are never echoed back on list — only on creation.
		out = append(out, map[string]any{
			"id":         h.ID,
			"url":        h.URL,
			"events":     events(h.Events),
			"created_at": h.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

// events normalizes a nil subscription list to the explicit "all" marker so the
// API response is unambiguous about what a webhook receives.
func events(e []string) []string {
	if len(e) == 0 {
		return []string{"*"}
	}
	return e
}
