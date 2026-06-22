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
	"time"

	"github.com/Arjun0606/smolbill/internal/ingest"
	"github.com/Arjun0606/smolbill/internal/payments"
	"github.com/Arjun0606/smolbill/internal/store"
)

// protocolVersion is the MCP revision we implement.
const protocolVersion = "2024-11-05"

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
		return s.reply(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
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
