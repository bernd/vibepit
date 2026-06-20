package sshd

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/session"
	charmssh "github.com/charmbracelet/ssh"
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

// TestRawNewlineNotCookedViaSSH guards against the SSH layer rewriting bare \n
// to \r\n on session output. Full-screen TUIs (e.g. agy/Antigravity) switch the
// PTY to raw mode and emit bare \n with relative cursor motion; an injected
// carriage return snaps the cursor to column 0 and corrupts cursor-positioned
// redraws.
//
// The session command disables output post-processing (stty -opost) and emits
// a bare \n between two markers. The bytes the client receives must contain the
// bare \n, never \r\n.
func TestRawNewlineNotCookedViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	// Raw-mode emitter: clear opost so the session PTY itself does not cook the
	// newline, then print MARK_A<bare-\n>MARK_B and linger briefly so the
	// output is delivered before the shell exits.
	mgr.Command = []string{"/bin/sh", "-c", "stty -opost; printf 'MARK_A\\nMARK_B'; sleep 0.3"}

	client := dialTestServer(t, mgr)

	sess, err := client.NewSession()
	require.NoError(t, err)

	var buf bytes.Buffer
	sess.Stdout = &buf
	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Shell())

	done := make(chan struct{})
	go func() { sess.Wait(); close(done) }() //nolint:errcheck
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit in time")
	}

	got := buf.String()
	require.Contains(t, got, "MARK_A", "expected emitter output in client stream")
	require.NotContains(t, got, "MARK_A\r\nMARK_B",
		"bare \\n was cooked to \\r\\n by the SSH layer")
	require.Contains(t, got, "MARK_A\nMARK_B",
		"client must receive the bare \\n exactly as the raw-mode app emitted it")
}

// syncBuf is a concurrency-safe buffer: the gossh client writes session output
// from its own goroutine while the test polls the accumulated string.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// dialTestServer starts an sshd Server backed by mgr on a loopback listener and
// returns a connected, authenticated client. All resources are torn down via
// t.Cleanup.
func dialTestServer(t *testing.T, mgr *session.Manager) *gossh.Client {
	t.Helper()
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() }) //nolint:errcheck

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
		Sessions:      mgr,
	})
	require.NoError(t, err)
	go srv.Serve(listener)            //nolint:errcheck
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)
	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() }) //nolint:errcheck
	return client
}

// TestWindowChangeResizesPTYViaSSH drives an SSH window-change through the
// custom session channel handler and asserts the session PTY is actually
// resized. The session prints its terminal size on a loop; after the resize the
// new dimensions appear. (gossh sends window-change as request order
// term=rows,cols; stty prints "rows cols".)
func TestWindowChangeResizesPTYViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	// Bounded poll so the size is reported deterministically (no WINCH-trap
	// timing dependency) and the detached session self-terminates after ~10s.
	mgr.Command = []string{"/bin/bash", "-c", "for i in $(seq 100); do stty size; sleep 0.1; done"}
	client := dialTestServer(t, mgr)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck

	var out syncBuf
	sess.Stdout = &out
	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Shell())

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "24 80")
	}, 3*time.Second, 20*time.Millisecond, "expected initial 24x80 size")

	require.NoError(t, sess.WindowChange(40, 100))

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "40 100")
	}, 3*time.Second, 20*time.Millisecond, "expected resized 40x100 size after window-change")
}

// TestRejectsDuplicatePtyReqViaSSH verifies a second pty-req is rejected — the
// guard that stops a later pty-req from orphaning the resize goroutine's
// channel.
func TestRejectsDuplicatePtyReqViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	client := dialTestServer(t, mgr)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck

	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}),
		"first pty-req should be accepted")
	require.Error(t, sess.RequestPty("xterm", 30, 100, gossh.TerminalModes{}),
		"second pty-req should be rejected")
}

// TestRejectsLateEnvViaSSH verifies an env request after shell dispatch is
// rejected — the guard that avoids racing the handler goroutine's read of
// sess.env.
func TestRejectsLateEnvViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	mgr.Command = []string{"/bin/sh", "-c", "sleep 5"}
	client := dialTestServer(t, mgr)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck

	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Setenv("EARLY", "1"), "env before shell should be accepted")
	require.NoError(t, sess.Shell())
	require.Error(t, sess.Setenv("LATE", "1"), "env after shell should be rejected")
}

// fakeChannel is a minimal gossh.Channel that captures stderr writes, for unit
// testing rawSession's stderr handling without a live SSH connection.
type fakeChannel struct{ stderr bytes.Buffer }

func (f *fakeChannel) Read([]byte) (int, error)                       { return 0, io.EOF }
func (f *fakeChannel) Write(p []byte) (int, error)                    { return len(p), nil }
func (f *fakeChannel) Close() error                                   { return nil }
func (f *fakeChannel) CloseWrite() error                              { return nil }
func (f *fakeChannel) SendRequest(string, bool, []byte) (bool, error) { return true, nil }
func (f *fakeChannel) Stderr() io.ReadWriter                          { return &f.stderr }

// TestRawSessionStderrCRLF verifies that rawSession cooks stderr to CRLF only
// when a PTY is present (the connected terminal is raw), mirroring charmssh's
// emulated-PTY Stderr. Without a PTY the exec client cooks output itself, so
// stderr must pass through raw.
func TestRawSessionStderrCRLF(t *testing.T) {
	withPty := &fakeChannel{}
	sp := &rawSession{Channel: withPty, winch: make(chan charmssh.Window, 1)}
	fmt.Fprint(sp.Stderr(), "create session: boom\n")
	require.Equal(t, "create session: boom\r\n", withPty.stderr.String(),
		"PTY-session stderr must use CRLF for a raw-mode client")

	noPty := &fakeChannel{}
	se := &rawSession{Channel: noPty}
	fmt.Fprint(se.Stderr(), "no command specified\n")
	require.Equal(t, "no command specified\n", noPty.stderr.String(),
		"non-PTY (exec) stderr must pass through raw")
}

// TestSelectorOutputUsesCRLFViaSSH guards the session-selector render path. The
// selector is a BubbleTea program written straight to the raw SSH channel (no
// real PTY in between). BubbleTea v2 forces newline-mapping on when it has no
// TTY input (cursed_renderer mapNl), so it emits bare \n for vertical motion
// and assumes the terminal cooks it to CRLF. With the \n->\r\n rewrite removed
// from the SSH layer, that assumption breaks and each per-tick redraw
// stair-steps to the right unless vibed cooks the selector output itself.
//
// A pre-seeded detached session makes the selector appear on connect; the test
// asserts every \n the client receives is preceded by \r.
func TestSelectorOutputUsesCRLFViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	// Short-lived so the detached session self-terminates after the test; alive
	// long enough that the selector lists it on connect.
	mgr.Command = []string{"/bin/sh", "-c", "sleep 5"}
	_, err := mgr.Create(80, 24, nil) // zero clients -> Detached -> selector shows
	require.NoError(t, err)

	client := dialTestServer(t, mgr)
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck

	var out syncBuf
	sess.Stdout = &out
	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Shell())

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "\n")
	}, 5*time.Second, 20*time.Millisecond, "selector should render a multi-line frame")

	got := out.String()
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' {
			require.Truef(t, i > 0 && got[i-1] == '\r',
				"selector output has a bare \\n at offset %d (stair-steps on a raw-mode client)", i)
		}
	}

	stdin.Write([]byte("q")) //nolint:errcheck
}
