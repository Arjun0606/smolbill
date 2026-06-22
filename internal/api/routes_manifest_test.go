package api

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRoutesMatchSDKManifest is the drift guard between the server and the SDKs.
// It extracts every route the server registers (from api.go) and asserts the API
// surface exactly equals sdk/routes.json — the contract the TypeScript and Python
// SDKs implement. The server-rendered /dashboard and /portal HTML are excluded;
// they are not part of the programmatic surface.
//
// If this fails, the API changed: update sdk/routes.json AND both SDKs so a new
// or renamed endpoint can never ship without the clients catching up.
func TestRoutesMatchSDKManifest(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	// mux.HandleFunc("GET /v1/usage/{customer_id}", ...)
	re := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) (\S+?)"`)
	server := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		method, path := m[1], m[2]
		if isSDKRoute(path) {
			server[method+" "+path] = true
		}
	}
	if len(server) == 0 {
		t.Fatal("parsed zero routes from api.go — regex likely needs updating")
	}

	var manifest struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
	}
	raw, err := os.ReadFile("../../sdk/routes.json")
	if err != nil {
		t.Fatalf("read sdk/routes.json: %v", err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse sdk/routes.json: %v", err)
	}
	declared := map[string]bool{}
	for _, r := range manifest.Routes {
		declared[r.Method+" "+r.Path] = true
	}

	if missing := diff(server, declared); len(missing) > 0 {
		t.Errorf("routes the server registers but sdk/routes.json is missing (SDKs are behind):\n  %s",
			strings.Join(missing, "\n  "))
	}
	if extra := diff(declared, server); len(extra) > 0 {
		t.Errorf("routes in sdk/routes.json the server no longer registers (SDKs reference dead endpoints):\n  %s",
			strings.Join(extra, "\n  "))
	}
}

// isSDKRoute reports whether a path is part of the programmatic API surface (the
// SDKs cover /v1/* and /healthz; /dashboard and /portal are server-rendered UI).
func isSDKRoute(path string) bool {
	return strings.HasPrefix(path, "/v1/") || path == "/healthz"
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
