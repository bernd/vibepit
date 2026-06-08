package sshd

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"
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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
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

func TestExecSessionRegistry(t *testing.T) {
	s := &Server{
		sessions:     session.NewManager(50),
		execSessions: make(map[uint64]*execSession),
	}

	t.Run("register and deregister", func(t *testing.T) {
		id := s.registerExec("echo hello")
		s.mu.Lock()
		assert.Len(t, s.execSessions, 1)
		assert.Equal(t, "echo hello", s.execSessions[id].command)
		assert.False(t, s.execSessions[id].startedAt.IsZero())
		s.mu.Unlock()

		s.deregisterExec(id)
		s.mu.Lock()
		assert.Empty(t, s.execSessions)
		s.mu.Unlock()
	})

	t.Run("multiple concurrent registrations", func(t *testing.T) {
		id1 := s.registerExec("cmd1")
		id2 := s.registerExec("cmd2")
		id3 := s.registerExec("cmd3")
		assert.NotEqual(t, id1, id2)
		assert.NotEqual(t, id2, id3)

		s.mu.Lock()
		assert.Len(t, s.execSessions, 3)
		s.mu.Unlock()

		s.deregisterExec(id2)
		s.mu.Lock()
		assert.Len(t, s.execSessions, 2)
		assert.Nil(t, s.execSessions[id2])
		s.mu.Unlock()

		s.deregisterExec(id1)
		s.deregisterExec(id3)
		s.mu.Lock()
		assert.Empty(t, s.execSessions)
		s.mu.Unlock()
	})

	t.Run("deregister nonexistent is safe", func(t *testing.T) {
		assert.NotPanics(t, func() {
			s.deregisterExec(9999)
		})
	})
}

func TestExecSessionRegistryConcurrent(t *testing.T) {
	s := &Server{
		sessions:     session.NewManager(50),
		execSessions: make(map[uint64]*execSession),
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			id := s.registerExec("stress")
			time.Sleep(time.Millisecond)
			s.deregisterExec(id)
		})
	}
	wg.Wait()

	s.mu.Lock()
	assert.Empty(t, s.execSessions)
	s.mu.Unlock()
}

func TestSessionCount(t *testing.T) {
	t.Run("empty state", func(t *testing.T) {
		s := &Server{
			sessions:     session.NewManager(50),
			execSessions: make(map[uint64]*execSession),
		}
		reply := s.sessionCount()
		assert.Equal(t, uint32(0), reply.PTYConns)
		assert.Equal(t, uint32(0), reply.AttachedPTY)
		assert.Equal(t, uint32(0), reply.DetachedPTY)
		assert.Equal(t, uint32(0), reply.ExecCount)
		assert.Empty(t, reply.DetachedInfo)
	})

	t.Run("exec sessions only", func(t *testing.T) {
		s := &Server{
			sessions:     session.NewManager(50),
			execSessions: make(map[uint64]*execSession),
		}
		id := s.registerExec("long-running")
		defer s.deregisterExec(id)

		reply := s.sessionCount()
		assert.Equal(t, uint32(1), reply.ExecCount)
		assert.Equal(t, uint32(0), reply.AttachedPTY)
	})

	t.Run("pty conns counted", func(t *testing.T) {
		s := &Server{
			sessions:     session.NewManager(50),
			execSessions: make(map[uint64]*execSession),
		}
		s.mu.Lock()
		s.ptyConns = 2
		s.mu.Unlock()

		reply := s.sessionCount()
		assert.Equal(t, uint32(2), reply.PTYConns)
	})

	t.Run("detached session counted and info populated", func(t *testing.T) {
		mgr := session.NewManager(50)
		s := &Server{
			sessions:     mgr,
			execSessions: make(map[uint64]*execSession),
		}

		// Create starts a session with zero clients → Detached.
		_, err := mgr.Create(80, 24, nil)
		require.NoError(t, err)

		reply := s.sessionCount()
		assert.Equal(t, uint32(0), reply.AttachedPTY)
		assert.Equal(t, uint32(1), reply.DetachedPTY)
		require.NotEmpty(t, reply.DetachedInfo)

		// Verify tab-delimited format: "id\tcommand\tage"
		parts := strings.SplitN(reply.DetachedInfo, "\t", 3)
		require.Len(t, parts, 3)
		assert.Equal(t, "session-1", parts[0])
		assert.NotEmpty(t, parts[1]) // command (shell path)
		assert.NotEmpty(t, parts[2]) // age (e.g. "<1m")
	})

	t.Run("attached session not in detached info", func(t *testing.T) {
		mgr := session.NewManager(50)
		s := &Server{
			sessions:     mgr,
			execSessions: make(map[uint64]*execSession),
		}

		sess, err := mgr.Create(80, 24, nil)
		require.NoError(t, err)

		// Attach a client → session becomes Attached.
		c := sess.Attach(80, 24)
		defer c.Close() //nolint:errcheck

		reply := s.sessionCount()
		assert.Equal(t, uint32(1), reply.AttachedPTY)
		assert.Equal(t, uint32(0), reply.DetachedPTY)
		assert.Empty(t, reply.DetachedInfo)
	})
}

func TestPTYConnCounterViaSSH(t *testing.T) {
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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	sess, err := client.NewSession()
	require.NoError(t, err)

	err = sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{})
	require.NoError(t, err)
	err = sess.Shell()
	require.NoError(t, err)

	// Give handlePTYSession time to increment the counter.
	require.Eventually(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.ptyConns > 0
	}, 2*time.Second, 10*time.Millisecond)

	srv.mu.Lock()
	assert.Equal(t, 1, srv.ptyConns)
	srv.mu.Unlock()

	// Close session; finishPTY should decrement before sess.Exit.
	sess.Close() //nolint:errcheck

	require.Eventually(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.ptyConns == 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestSessionCountViaSSH(t *testing.T) {
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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	ok, payload, err := client.SendRequest(sessionCountRequestType, true, nil)
	require.NoError(t, err)
	assert.True(t, ok)

	var reply SessionCountReply
	err = gossh.Unmarshal(payload, &reply)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), reply.AttachedPTY)
	assert.Equal(t, uint32(0), reply.DetachedPTY)
	assert.Equal(t, uint32(0), reply.ExecCount)
	assert.Equal(t, uint32(0), reply.PTYConns)
}

// A genuine shell exit must report a clean status (so the connecting client
// may offer to shut down the sandbox), while any forced detach must report a
// non-zero status so a client that merely lost its connection — e.g. across a
// laptop suspend/resume that tripped the keepalive — treats the result as a
// dropped connection and does not prompt to shut down.
func TestDetachExitCode(t *testing.T) {
	tests := []struct {
		name   string
		reason session.DetachReason
		want   int
	}{
		{"genuine shell exit", session.DetachNone, 0},
		{"keepalive timeout", session.DetachKeepalive, DisconnectExitCode},
		{"lost connection", session.DetachDisconnect, DisconnectExitCode},
		{"slow consumer", session.DetachSlowConsumer, DisconnectExitCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, detachExitCode(tt.reason))
		})
	}
}

// TestCleanExitViaSSH drives a real PTY session through the handler: the shell
// exits on its own, so the client's Wait must return a clean (nil) status and
// the session must not record the teardown as a disconnect.
func TestCleanExitViaSSH(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	mgr := session.NewManager(50)
	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
		Sessions:      mgr,
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close()      //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	sess, err := client.NewSession()
	require.NoError(t, err)

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	sess.Stdout = io.Discard
	sess.Stderr = io.Discard

	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Shell())

	// Tell the login shell to exit. A genuine exit drives the DetachNone path.
	_, err = stdin.Write([]byte("exit\n"))
	require.NoError(t, err)

	// A clean shell exit reports status 0, so Wait returns nil — this is the
	// only path on which the client offers to shut down the sandbox.
	assert.NoError(t, sess.Wait())

	// The teardown must not be mislabeled as a disconnect.
	require.Eventually(t, func() bool {
		for _, info := range mgr.List() {
			if info.Status == session.Exited {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	for _, info := range mgr.List() {
		assert.Equalf(t, session.DetachNone, info.LastDetachReason,
			"clean shell exit must not record a detach reason (session %s)", info.ID)
	}
}
