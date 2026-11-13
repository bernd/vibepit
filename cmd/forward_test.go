package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestParseForwardSpec(t *testing.T) {
	tests := []struct {
		in      string
		want    forwardSpec
		wantErr string
	}{
		{in: "3000", want: forwardSpec{"127.0.0.1", 3000, "localhost", 3000}},
		{in: "8080:3000", want: forwardSpec{"127.0.0.1", 8080, "localhost", 3000}},
		{in: "8080:127.0.0.1:3000", want: forwardSpec{"127.0.0.1", 8080, "127.0.0.1", 3000}},
		{in: "0.0.0.0:8080:localhost:3000", want: forwardSpec{"0.0.0.0", 8080, "localhost", 3000}},
		{in: "*:8080:localhost:3000", want: forwardSpec{"", 8080, "localhost", 3000}},
		{in: ":8080:localhost:3000", want: forwardSpec{"", 8080, "localhost", 3000}},
		{in: "[::1]:8080:[::1]:3000", want: forwardSpec{"::1", 8080, "::1", 3000}},
		{in: "0:3000", wantErr: "local port: 0 out of range"},
		{in: "", wantErr: "local port"},
		{in: "abc", wantErr: "not a number"},
		{in: "8080:0", wantErr: "remote port"},
		{in: "70000", wantErr: "out of range"},
		{in: "+80", wantErr: "not a number"},
		{in: "-0:3000", wantErr: "not a number"},
		{in: "8080::3000", wantErr: "empty host"},
		{in: "a:b:c:d:e", wantErr: "want"},
		{in: "[::1:8080:localhost:3000", wantErr: "unclosed"},
		{in: "80[80]", wantErr: "misplaced"},
		{in: "[]:8080:localhost:3000", wantErr: "empty brackets"},
		{in: "[::1]x:8080:localhost:3000", wantErr: "after ']'"},
		{in: "8080:10.0.0.1:3000", wantErr: "not a sandbox loopback"},
		{in: "8080:example.com:3000", wantErr: "not a sandbox loopback"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseForwardSpec(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// netDialer stands in for the SSH client: it dials directly, recording
// the remote addresses asked for.
type netDialer struct{ addrs chan string }

func (d netDialer) Dial(network, addr string) (net.Conn, error) {
	d.addrs <- addr
	return net.Dial(network, addr)
}

func TestPortForwarder(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer echo.Close() //nolint:errcheck
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close() //nolint:errcheck
				io.Copy(c, c)   //nolint:errcheck
			}()
		}
	}()
	echoPort := echo.Addr().(*net.TCPAddr).Port

	dialer := netDialer{addrs: make(chan string, 1)}
	pf, err := startForwards(dialer, []forwardSpec{{"127.0.0.1", 0, "127.0.0.1", echoPort}})
	require.NoError(t, err)
	t.Cleanup(func() { pf.Close() }) //nolint:errcheck // no-op after the Close below

	c, err := net.Dial("tcp", pf.listeners[0].Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() }) //nolint:errcheck
	_, err = c.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(c, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))
	assert.Equal(t, echo.Addr().String(), <-dialer.addrs)

	// Close must end the open connection, not wait for the client.
	require.NoError(t, pf.Close())
	_, err = c.Read(buf)
	assert.Error(t, err)

	// Check the listener itself rather than dialing its port, which
	// another process may have taken by now.
	_, err = pf.listeners[0].Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "listener should be closed")
}

// TestConnectForwardFlag checks that the help wrapper around the forward
// flag still collects every value and renders on one short line.
func TestConnectForwardFlag(t *testing.T) {
	connect := ConnectCommand()
	var got []string
	connect.Action = func(_ context.Context, cmd *cli.Command) error {
		got = cmd.StringSlice(forwardFlag)
		return nil
	}
	root := &cli.Command{Name: "vibepit", Commands: []*cli.Command{connect}}
	require.NoError(t, root.Run(context.Background(), []string{"vibepit", "connect", "-L", "3000", "--forward", "8080:3000"}))
	assert.Equal(t, []string{"3000", "8080:3000"}, got)

	var line string
	for _, f := range connect.Flags {
		if s := fmt.Sprint(f); strings.HasPrefix(s, "--forward") {
			line = s
		}
	}
	assert.Equal(t, "--forward [LOCAL:]PORT, -L [LOCAL:]PORT\t"+
		"Forward host port LOCAL (default: same as PORT) to sandbox port PORT while connected (repeatable)", line)
}

func TestForwardsRequest(t *testing.T) {
	req := forwardsRequest([]forwardSpec{
		{"127.0.0.1", 3000, "localhost", 3000},
		{"", 8080, "::1", 80},
	})
	assert.Equal(t, []string{"127.0.0.1:3000", ":8080"}, req.Local)
	assert.Equal(t, []string{"localhost:3000", "[::1]:80"}, req.Remote)
}

func TestStartForwardsPortInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close() //nolint:errcheck
	port := taken.Addr().(*net.TCPAddr).Port

	_, err = startForwards(netDialer{}, []forwardSpec{{"127.0.0.1", port, "localhost", 3000}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forward 127.0.0.1:")
}
