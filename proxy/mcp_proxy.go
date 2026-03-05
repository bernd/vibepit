package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxMCPBodySize = 10 << 20 // 10 MB

// knownMCPMethods lists JSON-RPC methods that are safe to forward.
// Unknown methods are rejected to prevent bypasses via future or
// vendor-prefixed methods.
var knownMCPMethods = map[string]bool{
	"initialize":               true,
	"initialized":              true,
	"ping":                     true,
	"notifications/cancelled":  true,
	"notifications/progress":   true,
	"tools/call":               true,
	"tools/list":               true,
	"resources/list":           true,
	"resources/read":           true,
	"resources/templates/list": true,
	"resources/subscribe":      true,
	"resources/unsubscribe":    true,
	"prompts/list":             true,
	"prompts/get":              true,
	"logging/setLevel":         true,
	"completion/complete":      true,
	"sampling/createMessage":   true,
	"roots/list":               true,
}

// MCPProxy is a reverse proxy for a single MCP server that filters tool calls.
type MCPProxy struct {
	serverName string
	upstream   string
	allowlist  *MCPToolAllowlist
	log        *LogBuffer
	client     *http.Client
}

func NewMCPProxy(serverName, upstream string, allowlist *MCPToolAllowlist, log *LogBuffer) *MCPProxy {
	return &MCPProxy{
		serverName: serverName,
		upstream:   upstream,
		allowlist:  allowlist,
		log:        log,
		client: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// jsonRPCRequest is the subset of a JSON-RPC request we need to inspect.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// toolCallParams extracts the tool name from a tools/call params object.
type toolCallParams struct {
	Name string `json:"name"`
}

// hopByHopHeaders are headers that MUST NOT be forwarded by proxies (RFC 7230 §6.1).
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func (p *MCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// GET requests (SSE event stream) pass through without inspection.
	// NOTE: SSE transport sends tools/list responses on this event stream,
	// so tools/list filtering only works for streamable-HTTP transport.
	// tools/call filtering works for both transports (POSTs are always
	// intercepted). SSE event stream filtering is deferred to a follow-up.
	if r.Method == http.MethodGet {
		p.forwardRequest(w, r, nil)
		return
	}

	// Read the request body with a size limit to prevent OOM.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPBodySize))
	if err != nil {
		http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadRequest)
		return
	}
	r.Body.Close()

	// SECURITY: Reject batch requests (JSON arrays). JSON-RPC 2.0 supports
	// batches, but they would bypass per-request tool filtering.
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeJSONRPCError(w, nil, -32600, "batch requests are not supported")
		return
	}

	// SECURITY: Reject unparseable requests. Never forward what we can't
	// inspect — parser differentials between Go and the upstream could
	// bypass filtering.
	var rpcReq jsonRPCRequest
	if err := json.Unmarshal(body, &rpcReq); err != nil {
		writeJSONRPCError(w, nil, -32700, "parse error")
		return
	}

	switch rpcReq.Method {
	case "tools/call":
		p.handleToolCall(w, r, body, &rpcReq)
	case "tools/list":
		p.handleToolsList(w, r, body, &rpcReq)
	default:
		// SECURITY: Only forward known MCP methods. Unknown or vendor-
		// prefixed methods could invoke tools in future protocol versions.
		if !knownMCPMethods[rpcReq.Method] {
			writeJSONRPCError(w, rpcReq.ID, -32601, fmt.Sprintf("method %q not allowed", rpcReq.Method))
			return
		}
		p.forwardRequest(w, r, body)
	}
}

func (p *MCPProxy) handleToolCall(w http.ResponseWriter, r *http.Request, body []byte, rpcReq *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
		writeJSONRPCError(w, rpcReq.ID, -32602, "invalid tool call params")
		return
	}

	if !p.allowlist.Allows(params.Name) {
		p.log.Add(LogEntry{
			Time:   time.Now(),
			Domain: p.serverName,
			Action: ActionBlock,
			Source: SourceMCP,
			Reason: fmt.Sprintf("tool %q not in allowlist", params.Name),
		})
		writeJSONRPCError(w, rpcReq.ID, -32601, fmt.Sprintf("tool %q is not allowed", params.Name))
		return
	}

	p.log.Add(LogEntry{
		Time:   time.Now(),
		Domain: p.serverName,
		Action: ActionAllow,
		Source: SourceMCP,
		Reason: fmt.Sprintf("tool %q allowed", params.Name),
	})

	p.forwardRequest(w, r, body)
}

func (p *MCPProxy) handleToolsList(w http.ResponseWriter, r *http.Request, body []byte, rpcReq *jsonRPCRequest) {
	// Use a deadline for the non-streaming tools/list round-trip.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Forward the request to get the full tools list.
	upstreamReq, err := http.NewRequestWithContext(ctx, r.Method, p.upstream+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"failed to create upstream request"}`, http.StatusInternalServerError)
		return
	}
	copyHeaders(upstreamReq.Header, r.Header)
	upstreamReq.URL.RawQuery = r.URL.RawQuery

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPBodySize))
	if err != nil {
		http.Error(w, `{"error":"failed to read upstream response"}`, http.StatusBadGateway)
		return
	}

	// Filter the tools list. On failure, return empty tools (fail-closed).
	filtered := p.filterToolsList(respBody)

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "application/json")
	// Remove stale Content-Length — the filtered body differs from upstream.
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	w.Write(filtered)
}

// filterToolsList removes disallowed tools from a tools/list response.
// On any parse failure, returns a response with an empty tools list
// (fail-closed: never leak unfiltered tool names).
func (p *MCPProxy) filterToolsList(body []byte) []byte {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return p.emptyToolsResponse(body)
	}

	resultRaw, ok := resp["result"]
	if !ok {
		return body
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return p.emptyToolsResponse(body)
	}

	toolsRaw, ok := result["tools"]
	if !ok {
		return body
	}

	var tools []map[string]any
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return p.emptyToolsResponse(body)
	}

	var filtered []map[string]any
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if p.allowlist.Allows(name) {
			filtered = append(filtered, tool)
		}
	}

	filteredJSON, err := json.Marshal(filtered)
	if err != nil {
		return p.emptyToolsResponse(body)
	}
	result["tools"] = filteredJSON
	newResult, err := json.Marshal(result)
	if err != nil {
		return p.emptyToolsResponse(body)
	}
	resp["result"] = newResult
	out, err := json.Marshal(resp)
	if err != nil {
		return p.emptyToolsResponse(body)
	}
	return out
}

func (p *MCPProxy) emptyToolsResponse(body []byte) []byte {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return []byte(`{"jsonrpc":"2.0","result":{"tools":[]}}`)
	}
	result := map[string]json.RawMessage{"tools": json.RawMessage("[]")}
	resultJSON, _ := json.Marshal(result)
	resp["result"] = resultJSON
	out, _ := json.Marshal(resp)
	return out
}

func (p *MCPProxy) forwardRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream+r.URL.Path, reqBody)
	if err != nil {
		http.Error(w, `{"error":"failed to create upstream request"}`, http.StatusInternalServerError)
		return
	}
	copyHeaders(upstreamReq.Header, r.Header)
	// Pass query string through.
	upstreamReq.URL.RawQuery = r.URL.RawQuery

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	// Stream the response for SSE support.
	if flusher, ok := w.(http.Flusher); ok {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		io.Copy(w, resp.Body)
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
