# MCP Proxy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an MCP-aware reverse proxy to vibepit that intercepts tool calls from sandboxed agents to external MCP servers and validates them against a per-server tool allowlist.

**Architecture:** Each configured MCP server gets a dedicated reverse proxy listener in the proxy container and a TCP forwarder on the host. The proxy parses JSON-RPC messages, filters `tools/call` requests and `tools/list` responses against glob-based tool allowlists, and forwards allowed traffic to the host via the network gateway. Configuration lives in `.vibepit/network.yaml`.

**Tech Stack:** `net/http` (reverse proxy), `encoding/json` (JSON-RPC parsing), existing `proxy` package patterns, `koanf` (config parsing).

---

### Task 1: MCP tool allowlist

**Files:**
- Create: `proxy/mcp_allowlist.go`
- Create: `proxy/mcp_allowlist_test.go`

**Step 1: Write the failing test**

Create `proxy/mcp_allowlist_test.go`:

```go
package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolAllowlist(t *testing.T) {
	al, err := NewMCPToolAllowlist([]string{
		"get_*",
		"search_in_files_by_text",
		"find_*",
		"list_directory_tree",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		tool string
		want bool
	}{
		{"exact match", "search_in_files_by_text", true},
		{"exact match 2", "list_directory_tree", true},
		{"glob prefix", "get_file_text_by_path", true},
		{"glob prefix 2", "get_symbol_info", true},
		{"glob find", "find_files_by_glob", true},
		{"not allowed", "execute_terminal_command", false},
		{"not allowed 2", "replace_text_in_file", false},
		{"not allowed 3", "rename_refactoring", false},
		{"empty tool", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.tool))
		})
	}
}

func TestMCPToolAllowlistEmpty(t *testing.T) {
	al, err := NewMCPToolAllowlist(nil)
	require.NoError(t, err)
	assert.False(t, al.Allows("anything"))
}

func TestMCPToolAllowlistValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{"valid exact", []string{"get_file"}, false},
		{"valid glob", []string{"get_*"}, false},
		{"empty entry", []string{""}, true},
		{"spaces", []string{"get file"}, true},
		{"bare star", []string{"*"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewMCPToolAllowlist(tt.entries)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestMCPTool -v`
Expected: FAIL — `NewMCPToolAllowlist` not found.

**Step 3: Implement**

Create `proxy/mcp_allowlist.go`:

```go
package proxy

import (
	"fmt"
	"strings"
)

// MCPToolAllowlist validates MCP tool names against glob patterns.
// Tool names are flat strings (no dots), so patterns use simple prefix
// glob matching: "get_*" matches any tool starting with "get_".
type MCPToolAllowlist struct {
	patterns []string
}

func NewMCPToolAllowlist(entries []string) (*MCPToolAllowlist, error) {
	for _, e := range entries {
		if err := validateToolPattern(e); err != nil {
			return nil, err
		}
	}
	lowered := make([]string, len(entries))
	for i, e := range entries {
		lowered[i] = strings.ToLower(e)
	}
	return &MCPToolAllowlist{patterns: lowered}, nil
}

func (al *MCPToolAllowlist) Allows(tool string) bool {
	if tool == "" {
		return false
	}
	tool = strings.ToLower(tool)
	for _, p := range al.patterns {
		if toolMatches(p, tool) {
			return true
		}
	}
	return false
}

// MatchedPattern returns the first pattern that matches tool, or "" if none.
func (al *MCPToolAllowlist) MatchedPattern(tool string) string {
	if tool == "" {
		return ""
	}
	tool = strings.ToLower(tool)
	for _, p := range al.patterns {
		if toolMatches(p, tool) {
			return p
		}
	}
	return ""
}

func toolMatches(pattern, tool string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == tool
	}
	// Only trailing * is supported: "get_*" matches "get_anything".
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(tool, prefix)
	}
	return false
}

func validateToolPattern(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid tool pattern: empty string")
	}
	if strings.Contains(entry, " ") {
		return fmt.Errorf("invalid tool pattern %q: spaces not allowed", entry)
	}
	if entry == "*" {
		return fmt.Errorf("invalid tool pattern: bare '*' is too broad")
	}
	// Only trailing * is allowed.
	starCount := strings.Count(entry, "*")
	if starCount > 1 {
		return fmt.Errorf("invalid tool pattern %q: at most one '*' allowed", entry)
	}
	if starCount == 1 && !strings.HasSuffix(entry, "*") {
		return fmt.Errorf("invalid tool pattern %q: '*' only allowed at the end", entry)
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestMCPTool -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/mcp_allowlist.go proxy/mcp_allowlist_test.go
git commit -m "proxy: add MCP tool allowlist with glob matching"
```

---

### Task 2: MCP config types

**Files:**
- Modify: `config/config.go:33-38` (ProjectConfig)
- Modify: `config/config.go:45-54` (MergedConfig)
- Modify: `proxy/server.go:20-31` (ProxyConfig)
- Create: `config/config_test.go` (if not exists, add test)

**Step 1: Write the failing test**

Add to config tests (create `config/mcp_test.go` if needed):

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMCPServers(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "network.yaml")
	err := os.WriteFile(projectPath, []byte(`
mcp-servers:
  - name: intellij
    url: http://127.0.0.1:6589
    transport: sse
    allow-tools:
      - "get_*"
      - "find_*"
`), 0o644)
	require.NoError(t, err)

	cfg, err := Load("", projectPath)
	require.NoError(t, err)
	require.Len(t, cfg.Project.MCPServers, 1)

	s := cfg.Project.MCPServers[0]
	assert.Equal(t, "intellij", s.Name)
	assert.Equal(t, "http://127.0.0.1:6589", s.URL)
	assert.Equal(t, "sse", s.Transport)
	assert.Equal(t, []string{"get_*", "find_*"}, s.AllowTools)
}

func TestMergeMCPServers(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "network.yaml")
	err := os.WriteFile(projectPath, []byte(`
mcp-servers:
  - name: intellij
    url: http://127.0.0.1:6589
    allow-tools:
      - "get_*"
`), 0o644)
	require.NoError(t, err)

	cfg, err := Load("", projectPath)
	require.NoError(t, err)

	merged, err := cfg.Merge(nil, nil)
	require.NoError(t, err)
	require.Len(t, merged.MCPServers, 1)
	assert.Equal(t, "intellij", merged.MCPServers[0].Name)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestLoadMCPServers -v`
Expected: FAIL — `MCPServers` field not found.

**Step 3: Add MCPServerConfig struct and fields**

In `config/config.go`, add the struct and update `ProjectConfig` and `MergedConfig`:

```go
type MCPServerConfig struct {
	Name       string   `koanf:"name"       json:"name"`
	URL        string   `koanf:"url"        json:"url"`
	Transport  string   `koanf:"transport"  json:"transport,omitempty"`
	AllowTools []string `koanf:"allow-tools" json:"allow-tools,omitempty"`
}
```

Add to `ProjectConfig`:

```go
MCPServers []MCPServerConfig `koanf:"mcp-servers"`
```

Add to `MergedConfig`:

```go
MCPServers []MCPServerConfig `json:"mcp-servers,omitempty"`
```

In `Merge()`, pass through the MCP servers (line ~120, add to the returned struct):

```go
MCPServers: c.Project.MCPServers,
```

Default `Transport` to `"sse"` if empty during merge:

```go
for i := range mcpServers {
	if mcpServers[i].Transport == "" {
		mcpServers[i].Transport = "sse"
	}
}
```

Also add to `proxy/server.go` `ProxyConfig`:

```go
MCPServers []MCPServerProxyConfig `json:"mcp-servers,omitempty"`
```

Where `MCPServerProxyConfig` is defined in a new section of `proxy/server.go`:

```go
type MCPServerProxyConfig struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Transport  string   `json:"transport"`
	AllowTools []string `json:"allow-tools"`
	Port       int      `json:"port"`
}
```

The `Port` field is set by the host before writing the proxy config JSON.

**Step 4: Run test to verify it passes**

Run: `go test ./config/ -run TestLoadMCPServers -v && go test ./config/ -run TestMergeMCPServers -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `make test`
Expected: PASS

**Step 6: Commit**

```bash
git add config/config.go config/mcp_test.go proxy/server.go
git commit -m "config: add MCP server configuration types"
```

---

### Task 3: Add SourceMCP to log buffer

**Files:**
- Modify: `proxy/log.go:15-20`

**Step 1: Add the constant**

In `proxy/log.go`, add to the Source constants:

```go
SourceMCP Source = "mcp"
```

**Step 2: Verify build**

Run: `go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add proxy/log.go
git commit -m "proxy: add SourceMCP log source constant"
```

---

### Task 4: MCP reverse proxy handler

This is the core component. It intercepts JSON-RPC over SSE/streamable-HTTP.

**Files:**
- Create: `proxy/mcp_proxy.go`
- Create: `proxy/mcp_proxy_test.go`

**Step 1: Write the failing test**

Create `proxy/mcp_proxy_test.go`:

```go
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPProxy_ToolCallAllowed(t *testing.T) {
	// Fake MCP server that returns a success response.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	proxy := NewMCPProxy("test-server", upstream.URL, al, log)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file_text_by_path","arguments":{"path":"/foo"}}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify log entry.
	entries := log.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, ActionAllow, entries[0].Action)
	assert.Equal(t, SourceMCP, entries[0].Source)
	assert.Equal(t, "test-server", entries[0].Domain)
}

func TestMCPProxy_ToolCallBlocked(t *testing.T) {
	// Upstream should never be called.
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	proxy := NewMCPProxy("test-server", upstream.URL, al, log)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_terminal_command","arguments":{"command":"rm -rf /"}}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.False(t, upstreamCalled)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify it's a JSON-RPC error.
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotNil(t, resp["error"])

	// Verify log entry.
	entries := log.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, ActionBlock, entries[0].Action)
	assert.Equal(t, SourceMCP, entries[0].Source)
}

func TestMCPProxy_NonToolCallPassesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	proxy := NewMCPProxy("test-server", upstream.URL, al, log)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Non-tool-call messages should not be logged.
	assert.Empty(t, log.Entries())
}

func TestMCPProxy_ToolsListFiltered(t *testing.T) {
	// Fake MCP server returns a tools/list response with multiple tools.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"tools": []map[string]any{
					{"name": "get_file_text_by_path", "description": "Get file"},
					{"name": "execute_terminal_command", "description": "Run command"},
					{"name": "get_symbol_info", "description": "Get symbol"},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	proxy := NewMCPProxy("test-server", upstream.URL, al, log)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	assert.Len(t, tools, 2) // Only get_* tools, not execute_terminal_command.

	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.(map[string]any)["name"].(string)
	}
	assert.Contains(t, names, "get_file_text_by_path")
	assert.Contains(t, names, "get_symbol_info")
	assert.NotContains(t, names, "execute_terminal_command")
}

func TestMCPProxy_SSEPassthrough(t *testing.T) {
	// Fake MCP server that returns an SSE stream.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file","arguments":{}}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, w.Body.String(), "hello")
}

func TestMCPProxy_GETPassthrough(t *testing.T) {
	// SSE transport uses GET for the event stream endpoint.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: endpoint\ndata: /messages\n\n"))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	req := httptest.NewRequest("GET", "/sse", nil)
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "endpoint")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestMCPProxy -v`
Expected: FAIL — `NewMCPProxy` not found.

**Step 3: Implement**

Create `proxy/mcp_proxy.go`:

```go
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
		client:     &http.Client{Timeout: 5 * time.Minute},
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

func (p *MCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// GET requests (SSE event stream) pass through without inspection.
	if r.Method == http.MethodGet {
		p.forwardRequest(w, r, nil)
		return
	}

	// Read the request body to inspect it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var rpcReq jsonRPCRequest
	if err := json.Unmarshal(body, &rpcReq); err != nil {
		// Not valid JSON-RPC — forward as-is.
		p.forwardRequest(w, r, body)
		return
	}

	switch rpcReq.Method {
	case "tools/call":
		p.handleToolCall(w, r, body, &rpcReq)
	case "tools/list":
		p.handleToolsList(w, r, body, &rpcReq)
	default:
		p.forwardRequest(w, r, body)
	}
}

func (p *MCPProxy) handleToolCall(w http.ResponseWriter, r *http.Request, body []byte, rpcReq *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
		p.forwardRequest(w, r, body)
		return
	}

	if !p.allowlist.Allows(params.Name) {
		p.log.Add(LogEntry{
			Time:   time.Now(),
			Domain: p.serverName,
			Port:   params.Name,
			Action: ActionBlock,
			Source: SourceMCP,
			Reason: "tool not in allowlist",
		})
		writeJSONRPCError(w, rpcReq.ID, -32601, fmt.Sprintf("tool %q is not allowed", params.Name))
		return
	}

	pattern := p.allowlist.MatchedPattern(params.Name)
	p.log.Add(LogEntry{
		Time:   time.Now(),
		Domain: p.serverName,
		Port:   params.Name,
		Action: ActionAllow,
		Source: SourceMCP,
		Reason: fmt.Sprintf("matched %q", pattern),
	})

	p.forwardRequest(w, r, body)
}

func (p *MCPProxy) handleToolsList(w http.ResponseWriter, r *http.Request, body []byte, rpcReq *jsonRPCRequest) {
	// Forward the request to get the full tools list.
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"failed to create upstream request"}`, http.StatusInternalServerError)
		return
	}
	copyHeaders(upstreamReq.Header, r.Header)

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read upstream response"}`, http.StatusBadGateway)
		return
	}

	// Try to filter the tools list.
	filtered, err := p.filterToolsList(respBody)
	if err != nil {
		// Can't parse — return original.
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(filtered)
}

func (p *MCPProxy) filterToolsList(body []byte) ([]byte, error) {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	resultRaw, ok := resp["result"]
	if !ok {
		return body, nil
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return body, nil
	}

	toolsRaw, ok := result["tools"]
	if !ok {
		return body, nil
	}

	var tools []map[string]any
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return body, nil
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
		return body, nil
	}
	result["tools"] = filteredJSON

	newResult, err := json.Marshal(result)
	if err != nil {
		return body, nil
	}
	resp["result"] = newResult

	return json.Marshal(resp)
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
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestMCPProxy -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/mcp_proxy.go proxy/mcp_proxy_test.go
git commit -m "proxy: add MCP reverse proxy with tool call filtering"
```

---

### Task 5: Start MCP proxy listeners in Server.Run

**Files:**
- Modify: `proxy/server.go:56-117`

**Step 1: Add MCP proxy startup to Server.Run**

After the existing service goroutines (HTTP proxy, DNS, control API), add MCP proxy listeners. Insert before the `select` block (line ~110):

```go
// Start MCP proxy listeners.
for _, mcpCfg := range s.config.MCPServers {
	mcpCfg := mcpCfg // capture
	go func() {
		al, err := NewMCPToolAllowlist(mcpCfg.AllowTools)
		if err != nil {
			errCh <- fmt.Errorf("MCP %s allowlist: %w", mcpCfg.Name, err)
			return
		}
		mcpProxy := NewMCPProxy(mcpCfg.Name, mcpCfg.URL, al, log)
		addr := fmt.Sprintf(":%d", mcpCfg.Port)
		fmt.Printf("proxy: MCP proxy for %q listening on %s -> %s\n", mcpCfg.Name, addr, mcpCfg.URL)
		errCh <- http.ListenAndServe(addr, mcpProxy)
	}()
}
```

Update `errCh` buffer size from `3` to `3+len(s.config.MCPServers)`:

```go
errCh := make(chan error, 3+len(s.config.MCPServers))
```

**Step 2: Verify build**

Run: `go build ./...`
Expected: success

**Step 3: Run existing tests**

Run: `make test`
Expected: PASS

**Step 4: Commit**

```bash
git add proxy/server.go
git commit -m "proxy: start MCP proxy listeners in Server.Run"
```

---

### Task 6: Host-side TCP forwarder

**Files:**
- Create: `cmd/tcpforward.go`
- Create: `cmd/tcpforward_test.go`

**Step 1: Write the failing test**

Create `cmd/tcpforward_test.go`:

```go
package cmd

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTCPForwarder(t *testing.T) {
	// Start an echo server simulating the MCP server on 127.0.0.1.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer echo.Close()

	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// Start the TCP forwarder.
	fwd, err := NewTCPForwarder("127.0.0.1:0", echo.Addr().String())
	require.NoError(t, err)
	defer fwd.Close()

	go fwd.Serve()

	// Connect through the forwarder.
	conn, err := net.Dial("tcp", fwd.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	msg := []byte("hello mcp")
	_, err = conn.Write(msg)
	require.NoError(t, err)

	buf := make([]byte, len(msg))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, msg, buf)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestTCPForwarder -v`
Expected: FAIL — `NewTCPForwarder` not found.

**Step 3: Implement**

Create `cmd/tcpforward.go`:

```go
package cmd

import (
	"io"
	"net"
)

// TCPForwarder listens on a local address and forwards connections to a target.
type TCPForwarder struct {
	ln     net.Listener
	target string
}

func NewTCPForwarder(listenAddr, target string) (*TCPForwarder, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return &TCPForwarder{ln: ln, target: target}, nil
}

func (f *TCPForwarder) Addr() net.Addr {
	return f.ln.Addr()
}

func (f *TCPForwarder) Close() error {
	return f.ln.Close()
}

func (f *TCPForwarder) Serve() error {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return err
		}
		go f.forward(conn)
	}
}

func (f *TCPForwarder) forward(src net.Conn) {
	defer src.Close()
	dst, err := net.Dial("tcp", f.target)
	if err != nil {
		return
	}
	defer dst.Close()
	go func() {
		io.Copy(dst, src)
		dst.Close()
	}()
	io.Copy(src, dst)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestTCPForwarder -v`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/tcpforward.go cmd/tcpforward_test.go
git commit -m "cmd: add TCP forwarder for MCP host connectivity"
```

---

### Task 7: Wire MCP into the run command

**Files:**
- Modify: `cmd/run.go` (port allocation, forwarders, proxy config, sandbox env vars)
- Modify: `container/client.go` (SandboxContainerConfig — add MCPEnvVars field)

**Step 1: Add MCPEnvVars to SandboxContainerConfig**

In `container/client.go`, add to `SandboxContainerConfig` (after the `User` field, line ~613):

```go
MCPEnvVars []string
```

In `CreateSandboxContainer`, append them to `env` (after the ColorTerm block, line ~635):

```go
env = append(env, cfg.MCPEnvVars...)
```

**Step 2: Add MCP wiring to RunAction in cmd/run.go**

After port allocation (line ~224, after `merged.ControlAPIPort = controlAPIPort`), add MCP port allocation and forwarder setup:

```go
// Allocate ports for MCP proxy listeners and start host-side TCP forwarders.
var mcpForwarders []*TCPForwarder
var mcpEnvVars []string
usedPorts := append(merged.AllowHostPorts, proxyPort, controlAPIPort)

for i := range merged.MCPServers {
	mcpPort, err := config.RandomProxyPort(usedPorts)
	if err != nil {
		return fmt.Errorf("MCP port for %s: %w", merged.MCPServers[i].Name, err)
	}
	usedPorts = append(usedPorts, mcpPort)
	merged.MCPServers[i].Port = mcpPort

	// Start host-side TCP forwarder: gateway-ip:mcpPort -> MCP server URL host:port.
	// The URL is parsed to extract host:port for the TCP target.
}
```

Parse each MCP server URL to get host:port for the TCP forwarder. After network creation (after `proxyIP := netInfo.ProxyIP`, line ~209), the gateway IP is available as `netInfo.GatewayIP` (or via `host-gateway` — check what `netInfo` provides). Start forwarders:

```go
for i, mcpCfg := range merged.MCPServers {
	// Parse URL to get host:port.
	u, err := url.Parse(mcpCfg.URL)
	if err != nil {
		return fmt.Errorf("MCP %s URL: %w", mcpCfg.Name, err)
	}
	target := u.Host
	if !strings.Contains(target, ":") {
		if u.Scheme == "https" {
			target += ":443"
		} else {
			target += ":80"
		}
	}

	listenAddr := fmt.Sprintf("%s:%d", netInfo.GatewayIP, mcpCfg.Port)
	fwd, err := NewTCPForwarder(listenAddr, target)
	if err != nil {
		return fmt.Errorf("MCP forwarder %s: %w", mcpCfg.Name, err)
	}
	mcpForwarders = append(mcpForwarders, fwd)
	go fwd.Serve()

	// Update the MCP server URL in config to point to the forwarder
	// as seen from the proxy container (gateway IP).
	merged.MCPServers[i].URL = fmt.Sprintf("http://%s:%d", netInfo.GatewayIP, mcpCfg.Port)

	// Build env var for sandbox.
	envName := "VIBEPIT_MCP_" + strings.ToUpper(strings.ReplaceAll(mcpCfg.Name, "-", "_"))
	mcpEnvVars = append(mcpEnvVars, fmt.Sprintf("%s=http://%s:%d", envName, proxyIP, mcpCfg.Port))

	tui.Status("MCP", "%s proxy on :%d -> %s", mcpCfg.Name, mcpCfg.Port, target)
}
defer func() {
	for _, fwd := range mcpForwarders {
		fwd.Close()
	}
}()
```

Pass `mcpEnvVars` to the sandbox config:

```go
MCPEnvVars: mcpEnvVars,
```

Add `"net/url"` and `"strings"` to imports if not present.

**Step 3: Check that GatewayIP is available**

Read `container/client.go` `CreateNetwork` return type. If `NetworkInfo` doesn't have `GatewayIP`, add it. The gateway IP is the host-side IP of the Docker bridge for this network — extract it from the Docker network inspect response.

**Step 4: Verify build**

Run: `go build ./...`
Expected: success

**Step 5: Run tests**

Run: `make test`
Expected: PASS

**Step 6: Commit**

```bash
git add cmd/run.go container/client.go
git commit -m "cmd: wire MCP proxy into run command with TCP forwarders"
```

---

### Task 8: Integration test

**Files:**
- Create: `proxy/mcp_proxy_integration_test.go`

**Step 1: Write an integration test that tests the full proxy chain**

This test simulates: fake MCP server -> MCP proxy -> tool call filtering.

```go
//go:build integration

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPProxyIntegration(t *testing.T) {
	// Fake MCP server with tools/list and tools/call.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)

		method, _ := req["method"].(string)
		switch method {
		case "tools/list":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "get_file_text_by_path", "description": "read file"},
						{"name": "execute_terminal_command", "description": "run cmd"},
						{"name": "find_files_by_glob", "description": "find files"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case "tools/call":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "tool result"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		default:
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]any{},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*", "find_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	mcpProxy := NewMCPProxy("test", upstream.URL, al, log)

	// Start the proxy on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	go http.Serve(ln, mcpProxy)
	proxyURL := "http://" + ln.Addr().String()

	t.Run("tools/list returns only allowed tools", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		resp, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		defer resp.Body.Close()

		var result map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		tools := result["result"].(map[string]any)["tools"].([]any)
		assert.Len(t, tools, 2) // get_file + find_files, not execute
	})

	t.Run("allowed tool call succeeds", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_file_text_by_path","arguments":{"path":"/test"}}}`
		resp, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		defer resp.Body.Close()

		var result map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		assert.Nil(t, result["error"])
		assert.NotNil(t, result["result"])
	})

	t.Run("blocked tool call returns error", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute_terminal_command","arguments":{"command":"ls"}}}`
		resp, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		defer resp.Body.Close()

		var result map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		assert.NotNil(t, result["error"])
		assert.Nil(t, result["result"])
	})

	t.Run("log entries recorded", func(t *testing.T) {
		entries := log.Entries()
		var allowed, blocked int
		for _, e := range entries {
			if e.Source == SourceMCP {
				switch e.Action {
				case ActionAllow:
					allowed++
				case ActionBlock:
					blocked++
				}
			}
		}
		assert.GreaterOrEqual(t, allowed, 1)
		assert.GreaterOrEqual(t, blocked, 1)
	})
}
```

**Step 2: Run integration test**

Run: `go test ./proxy/ -run TestMCPProxyIntegration -tags integration -v -timeout 30s`
Expected: PASS

**Step 3: Commit**

```bash
git add proxy/mcp_proxy_integration_test.go
git commit -m "proxy: add MCP proxy integration test"
```

---

### Summary

| # | Task | Files | Tests |
|---|---|---|---|
| 1 | MCP tool allowlist | `proxy/mcp_allowlist.go` | `proxy/mcp_allowlist_test.go` |
| 2 | Config types | `config/config.go`, `proxy/server.go` | `config/mcp_test.go` |
| 3 | SourceMCP log constant | `proxy/log.go` | — |
| 4 | MCP reverse proxy handler | `proxy/mcp_proxy.go` | `proxy/mcp_proxy_test.go` |
| 5 | Start MCP listeners in Server.Run | `proxy/server.go` | existing tests |
| 6 | Host TCP forwarder | `cmd/tcpforward.go` | `cmd/tcpforward_test.go` |
| 7 | Wire into run command | `cmd/run.go`, `container/client.go` | build + `make test` |
| 8 | Integration test | `proxy/mcp_proxy_integration_test.go` | integration |

Dependencies: Task 4 depends on tasks 1+3. Task 5 depends on tasks 2+4. Task 7 depends on tasks 5+6. Task 8 depends on task 4.
