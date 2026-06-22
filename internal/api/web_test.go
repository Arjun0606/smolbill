package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// getHTML fetches a page and returns status + body text.
func (h *serverHandle) getHTML(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(h.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestWalletTopupIdempotent(t *testing.T) {
	ts := newHandle(t)
	_, cust := ts.post(t, "/v1/customers", map[string]any{"name": "Acme"})
	cid := cust["id"].(string)

	// First top-up: $50.
	resp, body := ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{
		"amount": "50.00", "currency": "USD", "reason": "prepaid", "idempotency_key": "tx1",
	})
	if resp.StatusCode != http.StatusOK || body["balance"] != "50.00" {
		t.Fatalf("first topup: status %d body %v", resp.StatusCode, body)
	}
	// Same key again: must NOT double-credit.
	_, body = ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{
		"amount": "50.00", "currency": "USD", "reason": "prepaid", "idempotency_key": "tx1",
	})
	if body["balance"] != "50.00" {
		t.Fatalf("idempotent topup double-credited: balance %v, want 50.00", body["balance"])
	}
	// New key: credits again -> $90.
	_, body = ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{
		"amount": "40.00", "currency": "USD", "reason": "topup", "idempotency_key": "tx2",
	})
	if body["balance"] != "90.00" {
		t.Fatalf("second distinct topup: balance %v, want 90.00", body["balance"])
	}
}

func TestWalletCurrencyMismatchRejected(t *testing.T) {
	ts := newHandle(t)
	_, cust := ts.post(t, "/v1/customers", map[string]any{"name": "Acme"})
	cid := cust["id"].(string)
	ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{"amount": "10.00", "currency": "USD", "idempotency_key": "a"})
	resp, _ := ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{"amount": "10.00", "currency": "EUR", "idempotency_key": "b"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("currency mismatch status = %d, want 400", resp.StatusCode)
	}
}

func TestDashboardPagesRender(t *testing.T) {
	ts := newHandle(t)
	// Build a customer with usage + a finalized invoice so all pages have content.
	cid, subID := setupSubWithUsage(t, ts, map[string]int{"e1": 3000})
	ts.post(t, "/v1/wallet/"+cid+"/topup", map[string]any{"amount": "25.00", "currency": "USD", "idempotency_key": "w1"})
	_, fin := ts.post(t, "/v1/invoices/finalize", map[string]any{"subscription_id": subID})
	invID := fin["invoice_id"].(string)

	// Dashboard home lists the customer.
	if code, body := ts.getHTML(t, "/dashboard"); code != 200 || !strings.Contains(body, cid) {
		t.Fatalf("dashboard home: code %d, contains customer=%v", code, strings.Contains(body, cid))
	}
	// Customer page shows the projected bill and wallet balance.
	code, body := ts.getHTML(t, "/dashboard/customers/"+cid)
	if code != 200 {
		t.Fatalf("customer page code %d", code)
	}
	for _, want := range []string{"3.00", "25.00", "Event timeline", "reconcile"} {
		if !strings.Contains(body, want) {
			t.Fatalf("customer page missing %q", want)
		}
	}
	// Reconcile page renders a consistent verdict.
	if code, body := ts.getHTML(t, "/dashboard/invoices/"+invID+"/reconcile"); code != 200 || !strings.Contains(body, "consistent") {
		t.Fatalf("reconcile page: code %d, consistent=%v", code, strings.Contains(body, "consistent"))
	}
	// Portal renders with the distribution footer.
	if code, body := ts.getHTML(t, "/portal/"+cid); code != 200 || !strings.Contains(body, "metered by") {
		t.Fatalf("portal: code %d, footer=%v", code, strings.Contains(body, "metered by"))
	}
}
