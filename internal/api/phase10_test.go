package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (h *serverHandle) put(t *testing.T, path string, body any) (*http.Response, map[string]any) {
	return putReq(t, h.ts, path, body)
}

func putReq(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestDunningTemplatesListEditPreview(t *testing.T) {
	h := newHandle(t)

	// Defaults exist for all four events, none customized yet.
	_, list := h.get(t, "/v1/dunning/templates")
	tmpls := list["templates"].([]any)
	if len(tmpls) != 4 {
		t.Fatalf("want 4 default templates, got %d", len(tmpls))
	}
	for _, ti := range tmpls {
		if ti.(map[string]any)["customized"].(bool) {
			t.Error("a fresh account should have no customized templates")
		}
	}

	// Override the recovered copy.
	resp, _ := h.post(t, "/v1/dunning/preview", map[string]any{"event": "recovered"})
	_ = resp
	put, body := h.put(t, "/v1/dunning/templates/recovered", map[string]any{
		"subject": "Thanks {{.CustomerName}}!", "body": "We received {{.Amount}} {{.Currency}}.",
	})
	if put.StatusCode != 200 {
		t.Fatalf("set template status %d: %v", put.StatusCode, body)
	}

	// Now it shows as customized.
	_, list2 := h.get(t, "/v1/dunning/templates")
	var found bool
	for _, ti := range list2["templates"].([]any) {
		m := ti.(map[string]any)
		if m["event"] == "recovered" {
			found = true
			if !m["customized"].(bool) {
				t.Error("recovered should be customized after the override")
			}
		}
	}
	if !found {
		t.Fatal("recovered template missing from list")
	}

	// Preview renders the override with sample context.
	_, prev := h.post(t, "/v1/dunning/preview", map[string]any{"event": "recovered"})
	if !strings.Contains(prev["subject"].(string), "Thanks Acme Inc") {
		t.Errorf("preview subject = %q, want the customized + filled copy", prev["subject"])
	}
	if !strings.Contains(prev["body"].(string), "64.00 USD") {
		t.Errorf("preview body = %q, want sample amount filled", prev["body"])
	}
}

func TestSetTemplateRejectsMalformed(t *testing.T) {
	h := newHandle(t)
	resp, _ := h.put(t, "/v1/dunning/templates/payment_failed", map[string]any{
		"subject": "ok", "body": "{{.Unclosed",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("malformed template should be rejected (400), got %d", resp.StatusCode)
	}
}

func TestPreviewRejectsUnknownEvent(t *testing.T) {
	h := newHandle(t)
	resp, _ := h.post(t, "/v1/dunning/preview", map[string]any{"event": "not_an_event"})
	if resp.StatusCode != 400 {
		t.Fatalf("unknown event should be 400, got %d", resp.StatusCode)
	}
}
