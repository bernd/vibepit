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
