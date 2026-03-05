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
