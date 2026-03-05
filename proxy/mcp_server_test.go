package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartMCPProxyListener(t *testing.T) {
	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	al, err := NewMCPToolAllowlist([]string{"get_*"})
	require.NoError(t, err)
	log := NewLogBuffer(100)

	// Start a fake upstream.
	upstream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})}
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer upstreamLn.Close()
	go upstream.Serve(upstreamLn)

	mcpProxy := NewMCPProxy("test", "http://"+upstreamLn.Addr().String(), al, log)
	mcpLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer mcpLn.Close()
	go http.Serve(mcpLn, mcpProxy)

	// Verify it accepts connections and filters.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d", port),
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["result"])
}
