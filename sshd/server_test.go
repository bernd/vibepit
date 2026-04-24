package sshd

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

func TestServerAcceptsAuthorizedKey(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
		Sessions:      session.NewManager(50),
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close()      //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	session, err := client.NewSession()
	require.NoError(t, err)
	defer session.Close() //nolint:errcheck

	output, err := session.Output("echo hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(output))
}

func TestServerRejectsUnauthorizedKey(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	_, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	unauthorizedPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
		Sessions:      session.NewManager(50),
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close()      //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(unauthorizedPriv)
	require.NoError(t, err)

	_, err = gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	require.Error(t, err)
}

func TestClampPTYSize(t *testing.T) {
	tests := []struct {
		name     string
		cols     int
		rows     int
		wantCols uint16
		wantRows uint16
	}{
		{"zero falls back to default", 0, 0, defaultPTYCols, defaultPTYRows},
		{"negative falls back to default", -1, -10, defaultPTYCols, defaultPTYRows},
		{"normal values pass through", 120, 40, 120, 40},
		{"min size is honored", 1, 1, 1, 1},
		{"oversize is capped", 100000, 100000, maxPTYCols, maxPTYRows},
		{"max is exact passthrough", maxPTYCols, maxPTYRows, maxPTYCols, maxPTYRows},
		{"mixed: zero col, valid row", 0, 40, defaultPTYCols, 40},
		{"mixed: huge col, valid row", 99999, 40, maxPTYCols, 40},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := clampPTYSize(tc.cols, tc.rows)
			assert.Equal(t, tc.wantCols, cols)
			assert.Equal(t, tc.wantRows, rows)
		})
	}
}

// blockingReader returns one chunk per Read until close is closed, then EOF.
type blockingReader struct {
	chunks chan []byte
	closed chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	select {
	case data := <-b.chunks:
		return copy(p, data), nil
	case <-b.closed:
		return 0, io.EOF
	}
}

func TestSSHInputChannelExitsWhenDoneClosed(t *testing.T) {
	// Reader produces enough chunks to fill the 16-slot buffer plus one
	// more, so the goroutine must block on `ch <- data` waiting for a
	// consumer that never arrives. Closing done must unblock it.
	r := &blockingReader{
		chunks: make(chan []byte, 32),
		closed: make(chan struct{}),
	}
	for range 17 {
		r.chunks <- []byte("x")
	}

	done := make(chan struct{})
	ch := sshInputChannel(r, done)

	// Give the goroutine time to fill the buffer and block on send.
	time.Sleep(50 * time.Millisecond)

	// Closing done should unblock the goroutine and cause it to close ch.
	close(done)

	select {
	case _, ok := <-ch:
		// Either we drained a buffered item or saw the close — either is fine.
		// Keep draining until the channel is closed.
		if ok {
			for range ch {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("sshInputChannel did not exit after done closed")
	}
}

func TestSSHInputChannelClosesOnReadError(t *testing.T) {
	r := bytes.NewReader([]byte("hello"))
	done := make(chan struct{})
	defer close(done)

	ch := sshInputChannel(r, done)
	got, ok := <-ch
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), got)

	// Reader returned io.EOF; the goroutine should close ch.
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel should be closed after reader EOF")
	case <-time.After(time.Second):
		t.Fatal("sshInputChannel did not close after reader EOF")
	}
}
