package proxy

import (
	"encoding/json"
	"fmt"
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
	assert.Empty(t, entries[0].Port) // Port not used for MCP.
	assert.Contains(t, entries[0].Reason, "get_file_text_by_path")
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
	assert.Contains(t, entries[0].Reason, "execute_terminal_command")
}

func TestMCPProxy_BatchRequestRejected(t *testing.T) {
	// Upstream should never be called for batch requests.
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	// Batch request containing a blocked tool call.
	body := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_terminal_command","arguments":{}}}]`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.False(t, upstreamCalled)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotNil(t, resp["error"])
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, float64(-32600), errObj["code"])
}

func TestMCPProxy_InvalidJSONRejected(t *testing.T) {
	// Upstream should never be called for unparseable requests.
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	body := `{invalid json`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.False(t, upstreamCalled)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotNil(t, resp["error"])
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, float64(-32700), errObj["code"])
}

func TestMCPProxy_UnknownMethodRejected(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	body := `{"jsonrpc":"2.0","id":1,"method":"vendor/dangerous","params":{}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.False(t, upstreamCalled)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotNil(t, resp["error"])
}

func TestMCPProxy_KnownMethodPassesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	proxy := NewMCPProxy("test-server", upstream.URL, al, log)

	// Test both initialize and notifications/initialized (MCP lifecycle).
	for _, method := range []string{"initialize", "notifications/initialized"} {
		t.Run(method, func(t *testing.T) {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":{}}`, method)
			req := httptest.NewRequest("POST", "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			proxy.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
	// Known safe methods should not be logged.
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

func TestMCPProxy_ToolsListFilterFailureReturnsEmpty(t *testing.T) {
	// Upstream returns malformed tools/list response.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":"not_an_array"}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

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
	assert.Empty(t, tools) // Empty, not the original malformed data.
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

func TestMCPProxy_HopByHopHeadersNotForwarded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify hop-by-hop headers were stripped from the forwarded request.
		assert.Empty(t, r.Header.Get("Connection"))
		assert.Empty(t, r.Header.Get("Proxy-Authorization"))
		// Non-hop-by-hop should be forwarded.
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMCPProxy_ToolCallNullParams(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	proxy := NewMCPProxy("test-server", upstream.URL, al, NewLogBuffer(100))

	tests := []struct {
		name string
		body string
	}{
		{"null params", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":null}`},
		{"empty params", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`},
		{"missing name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamCalled = false
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			proxy.ServeHTTP(w, req)

			assert.False(t, upstreamCalled, "upstream should not be called")

			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.NotNil(t, resp["error"], "should return JSON-RPC error")
		})
	}
}

func TestMCPProxy_UpstreamBasePath(t *testing.T) {
	// Upstream MCP server at a non-root path with a query token.
	var receivedPath, receivedQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	// Upstream has a base path and query parameter.
	proxy := NewMCPProxy("test-server", upstream.URL+"/api/v1?token=abc", al, NewLogBuffer(100))

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/api/v1/messages", receivedPath)
	assert.Contains(t, receivedQuery, "token=abc")
}

func TestMCPProxy_UpstreamTrailingSlash(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)

	// Trailing slash on upstream should not cause double slashes.
	proxy := NewMCPProxy("test-server", upstream.URL+"/api/", al, NewLogBuffer(100))

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/api/messages", receivedPath)
}
