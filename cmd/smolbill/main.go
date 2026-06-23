// Command smolbill is the engine binary. Subcommands:
//
//	smolbill serve   run the HTTP /v1 API (Postgres if DATABASE_URL is set, else in-memory)
//	smolbill mcp     run the intent-only MCP server over stdio (for Claude/Cursor/agents)
//	smolbill demo    run the end-to-end deterministic pipeline walkthrough (no DB)
//
// One static binary, Postgres-only, no Kafka/ClickHouse/Temporal (build plan §6 #6).
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/api"
	"github.com/Arjun0606/smolbill/internal/domain"
	"github.com/Arjun0606/smolbill/internal/engine"
	"github.com/Arjun0606/smolbill/internal/id"
	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/invoice"
	"github.com/Arjun0606/smolbill/internal/mcp"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/payments/providers"
	"github.com/Arjun0606/smolbill/internal/reconcile"
	"github.com/Arjun0606/smolbill/internal/store"
	"github.com/Arjun0606/smolbill/internal/store/memory"
	"github.com/Arjun0606/smolbill/internal/store/postgres"
)

// version is stamped by GoReleaser at build time (-X main.version=...).
var version = "dev"

func main() {
	cmd := ""
	if len(os.Args) >= 2 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "version", "-v", "--version":
		fmt.Println("smolbill", version)
	case "serve":
		if err := runServe(); err != nil {
			log.Fatal(err)
		}
	case "mcp":
		if err := runMCP(); err != nil {
			log.Fatal(err)
		}
	case "demo":
		if err := runDemo(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "quickstart":
		if err := runQuickstart(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Println("smolbill — provably-correct usage billing")
	fmt.Println()
	fmt.Println("usage:")
	fmt.Println("  smolbill quickstart  seed a real account + serve it — see everything working in 60s")
	fmt.Println("  smolbill serve       run the HTTP /v1 API")
	fmt.Println("  smolbill mcp         run the MCP server over stdio (for Claude/Cursor/agents)")
	fmt.Println("  smolbill mcp --http  run the MCP server over Streamable HTTP on ADDR (remote clients)")
	fmt.Println("  smolbill demo        run the deterministic pipeline demo (no DB)")
	fmt.Println()
	fmt.Println("env:")
	fmt.Println("  DATABASE_URL        postgres DSN; if unset, uses an in-memory store")
	fmt.Println("  ADDR                HTTP listen address (default :8080)")
	fmt.Println("  SMOLBILL_PROCESSOR  payment rail: stripe|dodo|paddle|lemonsqueezy|polar|creem|razorpay|crypto")
	fmt.Println("                      (unset = auto-detect from whichever provider creds are set)")
	fmt.Println("  SMOLBILL_API_KEYS   comma-separated keys; set this to require auth on /v1 (unset = open)")
	fmt.Println("  SMOLBILL_RATE_LIMIT events/sec cap on POST /v1/events (optional)")
	fmt.Println("  SMOLBILL_DUNNING_INTERVAL  run the retry sweep on a cadence, e.g. 1h (needs a rail)")
}

// openStore builds the store from DATABASE_URL (Postgres) or falls back to
// in-memory. Logs go to stderr so the MCP stdio stream on stdout stays clean.
func openStore() (store.Store, func(), error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pg, err := postgres.New(context.Background(), dsn)
		if err != nil {
			return nil, nil, err
		}
		log.Printf("smolbill: using Postgres store")
		return pg, pg.Close, nil
	}
	log.Printf("smolbill: DATABASE_URL unset — using in-memory store (data is not persisted)")
	return memory.New(), func() {}, nil
}

// runServe boots the HTTP API.
func runServe() error {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	st, closeStore, err := openStore()
	if err != nil {
		return err
	}
	defer closeStore()

	srv := api.New(st, ingest.New(st, 0), nil)

	// Optional payment rail, selected from the environment (SMOLBILL_PROCESSOR or
	// auto-detected from whichever provider's credentials are set). When a rail is
	// configured, finalize pushes invoices to it; otherwise finalize is local-only
	// (the ledger is still written, just not pushed).
	proc, err := selectProcessor()
	if err != nil {
		return err
	}
	if proc != nil {
		srv.SetProcessor(proc)
		log.Printf("smolbill: %s payment rail enabled", proc.Name())
	} else {
		log.Printf("smolbill: no payment rail configured — finalize is local-only (no processor push)")
	}

	// API-key auth on /v1. Off by default so dev stays zero-config; loudly warn if
	// it's left open. Set SMOLBILL_API_KEYS=key1,key2 in production.
	if keys := os.Getenv("SMOLBILL_API_KEYS"); keys != "" {
		srv.SetAuthKeys(strings.Split(keys, ",")...)
		log.Printf("smolbill: API-key auth enabled on /v1")
	} else {
		log.Printf("smolbill: SMOLBILL_API_KEYS unset — /v1 is OPEN. Set it before exposing this publicly.")
	}

	// Optional rate limit on the ingest hot path: SMOLBILL_RATE_LIMIT = events/sec.
	if rl := os.Getenv("SMOLBILL_RATE_LIMIT"); rl != "" {
		rps, perr := strconv.ParseFloat(rl, 64)
		if perr != nil || rps <= 0 {
			return fmt.Errorf("SMOLBILL_RATE_LIMIT must be a positive number (events/sec), got %q", rl)
		}
		srv.SetRateLimit(rps, int(rps*2)+1)
		log.Printf("smolbill: rate limiting POST /v1/events at %g/s per key", rps)
	}

	// Optional dunning scheduler: SMOLBILL_DUNNING_INTERVAL (e.g. "1h") runs the
	// due-collection sweep on a cadence. Needs a payment rail; off by default.
	if iv := os.Getenv("SMOLBILL_DUNNING_INTERVAL"); iv != "" {
		d, perr := time.ParseDuration(iv)
		if perr != nil || d <= 0 {
			return fmt.Errorf("SMOLBILL_DUNNING_INTERVAL must be a positive duration (e.g. 1h), got %q", iv)
		}
		if proc == nil {
			log.Printf("smolbill: SMOLBILL_DUNNING_INTERVAL set but no payment rail — scheduler not started")
		} else {
			go func() {
				t := time.NewTicker(d)
				defer t.Stop()
				for range t.C {
					processed, faults, _, derr := srv.RunDueDunning(context.Background())
					if derr != nil {
						log.Printf("dunning scheduler: %v", derr)
					} else if processed > 0 {
						log.Printf("dunning scheduler: processed %d due collection(s), %d fault(s)", processed, faults)
					}
				}
			}()
			log.Printf("smolbill: dunning scheduler running every %s", d)
		}
	}

	log.Printf("smolbill: listening on %s (dedup window %s)", addr, ingest.DefaultDedupWindow)
	return http.ListenAndServe(addr, srv.Handler())
}

// runMCP serves the intent-only MCP tool surface. Default transport is stdio (for
// local editors: Claude Code/Desktop, Cursor, Cline, Windsurf, Zed). With
// `smolbill mcp --http`, it serves the Streamable HTTP transport on ADDR instead,
// so REMOTE clients (ChatGPT, claude.ai connectors, hosted agents) can connect.
// All logging goes to stderr; over stdio, stdout carries only JSON-RPC.
func runMCP() error {
	httpMode := false
	for _, a := range os.Args[2:] {
		if a == "--http" || a == "-http" {
			httpMode = true
		}
	}

	st, closeStore, err := openStore()
	if err != nil {
		return err
	}
	defer closeStore()
	srv := mcp.New(st, ingest.New(st, 0), nil)
	// Same optional rail as the HTTP server: when one is configured, the agent can
	// verify invoices against the processor and run dunning.
	proc, err := selectProcessor()
	if err != nil {
		return err
	}
	if proc != nil {
		srv.SetProcessor(proc)
	}

	if httpMode {
		addr := os.Getenv("ADDR")
		if addr == "" {
			addr = ":8080"
		}
		log.Printf("smolbill: MCP server ready on http://%s/mcp (Streamable HTTP)", addr)
		return http.ListenAndServe(addr, srv.HTTPHandler())
	}

	log.Printf("smolbill: MCP server ready on stdio")
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}

// selectProcessor resolves the payment rail from the environment via the provider
// registry. Returns (nil, nil) when no rail is configured (finalize stays
// local-only). Any rail whose collection is managed externally (a Merchant of
// Record like Dodo/Paddle/Lemon Squeezy/Polar/Creem, or the push-only crypto rail)
// handles its own dunning, so smolbill's internal dunning should stay off for those.
func selectProcessor() (payments.Processor, error) {
	proc, err := providers.Select()
	if err != nil {
		return nil, err
	}
	switch name := procName(proc); name {
	case "dodo", "paddle", "lemonsqueezy", "polar", "creem", "crypto":
		log.Printf("smolbill: %q manages collection externally — keep smolbill dunning disabled for this rail", name)
	}
	return proc, nil
}

func procName(p payments.Processor) string {
	if p == nil {
		return ""
	}
	return p.Name()
}

func dec(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}
func decp(s string) *decimal.Decimal { v := dec(s); return &v }

func runDemo() error {
	st := memory.New()
	ing := ingest.New(st, 0) // default published dedup window

	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// 1. Customer.
	st.PutCustomer(domain.Customer{ID: "cus_acme", ExternalID: "acme", Name: "Acme AI", CreatedAt: periodStart})

	// 2. Meter: sum of tokens.
	st.PutMeter(domain.Meter{Code: "tokens", Name: "LLM tokens", Aggregation: domain.AggSum, PropertyKey: "n"})

	// 3. Plan: $49 base (flat, prorated) + graduated token pricing.
	plan := domain.Plan{
		ID: "plan_pro", Name: "Pro", Version: 1, CreatedAt: periodStart,
		Prices: []domain.Price{
			{ID: "pr_base", PlanID: "plan_pro", Model: domain.ModelFlat, Currency: "USD", FlatAmount: dec("49.00")},
			{ID: "pr_tok", PlanID: "plan_pro", MeterCode: "tokens", Model: domain.ModelTieredGraduated, Currency: "USD",
				Tiers: []domain.Tier{
					{UpTo: decp("100000"), UnitAmount: dec("0.00002")}, // first 100k @ $0.00002
					{UpTo: nil, UnitAmount: dec("0.00001")},            // beyond @ $0.00001
				}},
		},
	}
	st.PutPlan(plan)

	// 4. Subscription, started mid-period (2026-06-16) to show proration.
	started := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub := domain.Subscription{
		ID: "sub_acme", CustomerID: "cus_acme", PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: periodStart, CurrentPeriodEnd: periodEnd,
		StartedAt: started,
	}
	st.PutSubscription(sub)

	// 5. Ingest usage events — including a deliberate duplicate and a late one.
	type raw struct {
		key string
		t   time.Time
		n   int
	}
	usage := []raw{
		{"evt_1", time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC), 40000},
		{"evt_2", time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC), 50000},
		{"evt_1", time.Date(2026, 6, 18, 9, 5, 0, 0, time.UTC), 99999}, // duplicate key -> ignored
		{"evt_3", time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC), 30000},
	}
	accepted, dupes := 0, 0
	for _, u := range usage {
		now := u.t.Add(2 * time.Second) // ingestion clock slightly after event
		_, err := ing.Accept(domain.Event{
			IdempotencyKey: u.key, CustomerID: "cus_acme", MeterCode: "tokens",
			EventTime: u.t, Properties: map[string]any{"n": u.n},
		}, now)
		switch {
		case err == nil:
			accepted++
		case isDup(err):
			dupes++
		default:
			return err
		}
	}

	fmt.Printf("Ingested %d events (%d duplicate key ignored — idempotent, window %s)\n\n",
		accepted, dupes, ing.Window())

	// 6. Deterministic invoice preview.
	events, err := st.EventsForCustomer("cus_acme")
	if err != nil {
		return err
	}
	meters, err := st.Meters()
	if err != nil {
		return err
	}
	res, err := invoice.Calculate(sub, plan, meters, events)
	if err != nil {
		return err
	}

	printInvoice(res, started, periodStart, periodEnd)

	// 7. Determinism proof: recompute, hash must match.
	res2, _ := invoice.Calculate(sub, plan, meters, events)
	fmt.Printf("\nDeterminism check: recomputed hash %s\n", matchLabel(res.Hash, res2.Hash))
	return nil
}

func printInvoice(res invoice.Result, started, periodStart, periodEnd time.Time) {
	inv := res.Invoice
	fmt.Printf("INVOICE  customer=%s  period=%s .. %s\n",
		inv.CustomerID, periodStart.Format("2006-01-02"), periodEnd.Format("2006-01-02"))
	fmt.Printf("         subscription started %s (mid-period -> flat fee prorated)\n\n", started.Format("2006-01-02"))

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "LINE\tMETER\tMODEL\tEVENTS\tQUANTITY\tPRORATION\tAMOUNT")
	for i, tr := range res.Traces {
		meterName := tr.MeterCode
		if meterName == "" {
			meterName = "(base fee)"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\t%s\t$%s\n",
			i+1, meterName, tr.PriceModel, tr.RawEventCount,
			tr.MeterValue.String(), tr.ProrationFactor.StringFixed(4), tr.Amount.StringFixed(2))
	}
	fmt.Fprintf(w, "\t\t\t\t\t\tTOTAL  $%s %s\n", inv.Total.StringFixed(2), inv.Currency)
	w.Flush()

	fmt.Printf("\nReconciliation hash: %s\n", res.Hash)
	fmt.Println("  ^ raw events -> meter value -> invoice line, provable & tamper-evident (Phase 2 ledger).")
}

func isDup(err error) bool { return err != nil && err.Error() == ingest.ErrDuplicate.Error() }

func matchLabel(a, b string) string {
	if a == b {
		return "MATCH ✓ (meter and invoice provably agree)"
	}
	return "MISMATCH ✗"
}

// seedExample populates st with a realistic "Acme AI" account: a Pro plan
// ($49 base + graduated token pricing), a mid-period subscription, and usage
// events (one duplicate key that is ignored, one normal). Returns the
// subscription plus how many events were accepted / deduped. Used by quickstart.
func seedExample(st store.Store, ing *ingest.Ingester) (sub domain.Subscription, accepted, dupes int, err error) {
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err = st.PutCustomer(domain.Customer{ID: "cus_acme", ExternalID: "acme", Name: "Acme AI", CreatedAt: periodStart}); err != nil {
		return
	}
	if err = st.PutMeter(domain.Meter{Code: "tokens", Name: "LLM tokens", Aggregation: domain.AggSum, PropertyKey: "n"}); err != nil {
		return
	}
	plan := domain.Plan{
		ID: "plan_pro", Name: "Pro", Version: 1, CreatedAt: periodStart,
		Prices: []domain.Price{
			{ID: "pr_base", PlanID: "plan_pro", Model: domain.ModelFlat, Currency: "USD", FlatAmount: dec("49.00")},
			{ID: "pr_tok", PlanID: "plan_pro", MeterCode: "tokens", Model: domain.ModelTieredGraduated, Currency: "USD",
				Tiers: []domain.Tier{
					{UpTo: decp("100000"), UnitAmount: dec("0.00002")},
					{UpTo: nil, UnitAmount: dec("0.00001")},
				}},
		},
	}
	if err = st.PutPlan(plan); err != nil {
		return
	}
	started := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub = domain.Subscription{
		ID: "sub_acme", CustomerID: "cus_acme", PlanID: plan.ID, PlanVersion: plan.Version,
		Status: domain.SubActive, CurrentPeriodStart: periodStart, CurrentPeriodEnd: periodEnd, StartedAt: started,
	}
	if err = st.PutSubscription(sub); err != nil {
		return
	}
	usage := []struct {
		key string
		t   time.Time
		n   int
	}{
		{"evt_1", time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC), 40000},
		{"evt_2", time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC), 50000},
		{"evt_1", time.Date(2026, 6, 18, 9, 5, 0, 0, time.UTC), 99999}, // duplicate key -> ignored
		{"evt_3", time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC), 30000},
	}
	for _, u := range usage {
		_, aerr := ing.Accept(domain.Event{
			IdempotencyKey: u.key, CustomerID: "cus_acme", MeterCode: "tokens",
			EventTime: u.t, Properties: map[string]any{"n": u.n},
		}, u.t.Add(2*time.Second))
		switch {
		case aerr == nil:
			accepted++
		case isDup(aerr):
			dupes++
		default:
			return sub, accepted, dupes, aerr
		}
	}
	return sub, accepted, dupes, nil
}

// runQuickstart seeds a real account, pre-finalizes one invoice (so reconcile and
// the dashboard work instantly), then serves the API and prints a guided tour.
// This is the dead-simple onboarding the enterprise incumbents can't offer.
func runQuickstart() error {
	st := memory.New()
	ing := ingest.New(st, 0)
	sub, accepted, dupes, err := seedExample(st, ing)
	if err != nil {
		return err
	}

	// Pre-finalize an invoice so /reconcile + /verify + the dashboard light up.
	res, err := engine.Compute(st, sub)
	if err != nil {
		return err
	}
	inv := res.Invoice
	inv.ID = id.New("inv")
	inv.Status = "finalized"
	if err := st.SaveFinalizedInvoice(inv, reconcile.LedgerFromResult(inv.ID, res)); err != nil {
		return err
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	base := "http://localhost" + addr

	fmt.Println("smolbill quickstart — a real account, seeded and ready.")
	fmt.Printf("  Acme AI: Pro plan ($49 base + graduated tokens), %d events (%d duplicate ignored), 1 finalized invoice.\n\n", accepted, dupes)
	fmt.Println("open these (the server is running):")
	fmt.Printf("  dashboard             %s/dashboard\n", base)
	fmt.Printf("  customer portal       %s/portal/cus_acme\n", base)
	fmt.Printf("  live usage            %s/v1/usage/cus_acme\n", base)
	fmt.Printf("  time-travel the bill  %s/v1/usage/cus_acme?as_of=2026-06-20T00:00:00Z\n", base)
	fmt.Printf("  reconcile (headline)  %s/v1/reconcile/%s\n\n", base, inv.ID)
	fmt.Println("simulate a price change WITHOUT committing it (the AI sandbox):")
	fmt.Printf("  curl -s -X POST %s/v1/invoices/simulate -H 'content-type: application/json' \\\n", base)
	fmt.Println(`    -d '{"customer_id":"cus_acme","plan":{"name":"Pro","prices":[{"model":"flat","currency":"USD","flat_amount":"49.00"},{"meter_code":"tokens","model":"per_unit","currency":"USD","unit_amount":"0.00003"}]}}'`)
	fmt.Println()
	fmt.Println("or drive it all from your AI agent:  smolbill mcp")
	fmt.Println()
	log.Printf("smolbill: serving on %s — ctrl-c to stop", addr)

	return http.ListenAndServe(addr, api.New(st, ing, nil).Handler())
}
