// Package mcp is smolbill's Model Context Protocol server — the "AI sets the
// rule" half of the thesis (build plan §0.5, §6 #1). It exposes a small,
// intent-only tool surface so an agent (Claude, Cursor, …) can configure billing
// in plain language: create meters and plans, attach plans, set spend caps, and
// read usage / preview invoices.
//
// Crucially, the agent NEVER does money math. There is no charge() or
// calculate_bill() tool. Every tool passes *intent*; the deterministic engine
// (internal/engine, internal/invoice) computes every cent. A hallucinated
// decimal in billing ends a business relationship, so the model is structurally
// kept away from the arithmetic.
//
// Transport is JSON-RPC 2.0 over stdio with newline-delimited messages (the MCP
// stdio transport). Implemented directly — no SDK — to keep the single binary
// and make the wire format auditable.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/store"
)

// defaultProtocolVersion is the MCP revision we advertise when a client does not
// request one. Our tool surface (initialize / tools.list / tools.call / ping) is
// stable across MCP revisions, so we negotiate by ECHOING the client's requested
// version when it is one we recognize — this is what lets newer, stricter clients
// (which reject a mismatched version) connect, instead of being pinned to an old
// revision. Unknown/absent versions fall back to this default.
const defaultProtocolVersion = "2025-06-18"

// supportedProtocolVersions are the MCP revisions we will echo back to a client.
// All are wire-compatible with our handlers; the set just bounds what we'll claim.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// negotiateVersion picks the protocol version to report in the initialize result:
// the client's requested version if we recognize it, else our default.
func negotiateVersion(requested string) string {
	if supportedProtocolVersions[requested] {
		return requested
	}
	return defaultProtocolVersion
}

// Server serves the MCP tool surface over a single stdio connection.
type Server struct {
	store store.Store
	ing   *ingest.Ingester
	now   func() time.Time
	proc  payments.Processor // optional rail; enables verify/collect/dunning tools
	tools []tool
}

// New builds an MCP server over the given store. A nil clock defaults to UTC now.
func New(st store.Store, ing *ingest.Ingester, clock func() time.Time) *Server {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	s := &Server{store: st, ing: ing, now: clock}
	s.tools = s.buildTools()
	return s
}

// SetProcessor attaches a payment rail so the agent can verify invoices against
// the processor and run dunning. Without it, those tools return a clear error.
func (s *Server) SetProcessor(p payments.Processor) { s.proc = p }

// --- JSON-RPC envelope ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Serve runs the JSON-RPC loop until r is exhausted (stdin closed) or ctx is
// cancelled. Messages are newline-delimited JSON.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriter(w)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if resp, ok := s.handle(ctx, line); ok {
				if err := writeMessage(bw, resp); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// HTTPHandler exposes the same tool surface over the MCP Streamable HTTP transport
// so REMOTE clients (ChatGPT, claude.ai connectors, hosted agents) can connect —
// not just local editors over stdio. It is deliberately stateless: each POST
// carries one JSON-RPC message, and the response is returned synchronously as
// application/json. We do not open an SSE stream because this server never pushes
// server-initiated messages; a GET (the stream-open verb) therefore returns 405.
//
// Per the spec, a notification (no id) yields 202 Accepted with an empty body. Any
// Mcp-Session-Id the client sends is echoed back; we keep no server-side session.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			w.Header().Set("Mcp-Session-Id", sid)
		}
		switch r.Method {
		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
			if err != nil {
				writeHTTPError(w, http.StatusBadRequest, codeParse, "read error")
				return
			}
			resp, ok := s.handle(r.Context(), body)
			if !ok {
				// Notification: accepted, nothing to return.
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case http.MethodGet:
			// We don't support the optional server->client SSE stream.
			http.Error(w, "method not allowed: this server has no SSE stream", http.StatusMethodNotAllowed)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// writeHTTPError emits a JSON-RPC error object with a 200 body — JSON-RPC carries
// its own error channel, so transport stays 200 while the error rides in the body.
func writeHTTPError(w http.ResponseWriter, _ int, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{code, msg}})
}

func writeMessage(bw *bufio.Writer, resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := bw.Write(append(b, '\n')); err != nil {
		return err
	}
	return bw.Flush()
}

// handle processes one message. ok=false means it was a notification (no reply).
func (s *Server) handle(ctx context.Context, line []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeParse, "parse error"}}, true
	}
	// Notifications have no id and expect no response.
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		var ip struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &ip)
		return s.reply(req.ID, map[string]any{
			"protocolVersion": negotiateVersion(ip.ProtocolVersion),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "smolbill", "version": "0.1.0"},
		}), true
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, false
	case "ping":
		return s.reply(req.ID, map[string]any{}), true
	case "tools/list":
		return s.reply(req.ID, map[string]any{"tools": s.toolList()}), true
	case "tools/call":
		return s.callTool(ctx, req), true
	default:
		if isNotification {
			return rpcResponse{}, false
		}
		return s.fail(req.ID, codeMethodNotFound, "method not found: "+req.Method), true
	}
}

func (s *Server) reply(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) fail(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}}
}

// callTool dispatches a tools/call request. Per MCP, tool execution errors are
// returned inside the result with isError=true (not as JSON-RPC errors), so the
// agent can read and react to them.
func (s *Server) callTool(ctx context.Context, req rpcRequest) rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.fail(req.ID, codeInvalidParams, "invalid params")
	}
	for _, t := range s.tools {
		if t.name == p.Name {
			text, err := t.handler(ctx, p.Arguments)
			if err != nil {
				return s.reply(req.ID, toolResult(err.Error(), true))
			}
			return s.reply(req.ID, toolResult(text, false))
		}
	}
	return s.reply(req.ID, toolResult(fmt.Sprintf("unknown tool %q", p.Name), true))
}

// toolResult builds an MCP tool result with a single text content block.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, map[string]any{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.inputSchema,
		})
	}
	return out
}
