package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/colorprofile"

	charmssh "github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Config holds configuration for the SSH server.
type Config struct {
	HostKeyPEM    []byte
	AuthorizedKey []byte
	Sessions      *session.Manager
}

// Server wraps a charmbracelet/ssh server with public key authentication.
type Server struct {
	server   *charmssh.Server
	sessions *session.Manager
}

// keepaliveRequestType is the SSH global request name used by both the PTY
// and exec keepalive paths. It matches the OpenSSH client/server convention.
const keepaliveRequestType = "keepalive@openssh.com"

// Connection timing constants. Both keepalive intervals must stay safely
// below idleConnectionTimeout so silent active sessions keep the
// connection's deadline refreshed; the timeout itself closes stalled
// handshakes to prevent a local DoS via leaked goroutines/fds.
const (
	idleConnectionTimeout = 2 * time.Minute
	execKeepaliveInterval = 30 * time.Second
)

// PTY size bounds. SSH clients can request arbitrary uint32 dimensions, but
// the VTE buffer allocates proportional to width*height, so unclamped values
// let an authenticated peer force large allocations.
const (
	defaultPTYCols = 80
	defaultPTYRows = 24
	maxPTYCols     = 2048
	maxPTYRows     = 2048
)

// NewServer creates a new SSH server that authenticates using the given
// authorized key and presents the given host key.
func NewServer(cfg Config) (*Server, error) {
	authorizedKey, _, _, _, err := gossh.ParseAuthorizedKey(cfg.AuthorizedKey)
	if err != nil {
		return nil, fmt.Errorf("parse authorized key: %w", err)
	}

	s := &Server{
		sessions: cfg.Sessions,
	}

	srv := &charmssh.Server{
		Handler:     s.handleSession,
		IdleTimeout: idleConnectionTimeout,
	}

	if err := srv.SetOption(charmssh.HostKeyPEM(cfg.HostKeyPEM)); err != nil {
		return nil, fmt.Errorf("set host key: %w", err)
	}

	if err := srv.SetOption(charmssh.PublicKeyAuth(func(_ charmssh.Context, key charmssh.PublicKey) bool {
		return charmssh.KeysEqual(key, authorizedKey)
	})); err != nil {
		return nil, fmt.Errorf("set public key auth: %w", err)
	}

	s.server = srv
	return s, nil
}

// Serve accepts incoming connections on the listener.
func (s *Server) Serve(l net.Listener) error {
	return s.server.Serve(l)
}

// Close immediately closes all active listeners and connections.
func (s *Server) Close() error {
	return s.server.Close()
}

func (s *Server) handleSession(sess charmssh.Session) {
	ptyReq, winCh, isPty := sess.Pty()
	if isPty {
		s.handlePTYSession(sess, ptyReq, winCh)
	} else {
		handleExecSession(sess)
	}
}

func (s *Server) handlePTYSession(sess charmssh.Session, ptyReq charmssh.Pty, winCh <-chan charmssh.Window) {
	cols, rows := clampPTYSize(ptyReq.Window.Width, ptyReq.Window.Height)
	mgr := s.sessions

	// Build environment from SSH session (includes TERM, etc.).
	// SSH clients typically don't forward COLORTERM. Fall back to the
	// container's own value so the TUI can detect TrueColor support.
	sshEnv := sess.Environ()
	sshEnv = append(sshEnv, fmt.Sprintf("TERM=%s", ptyReq.Term))
	hasColorterm := slices.ContainsFunc(sshEnv, func(e string) bool {
		return strings.HasPrefix(e, "COLORTERM=")
	})
	if !hasColorterm {
		if ct := os.Getenv("COLORTERM"); ct != "" {
			sshEnv = append(sshEnv, "COLORTERM="+ct)
		}
	}

	allSessions := mgr.List()

	// Only detached sessions are relevant for the selector.
	var detached []session.SessionInfo
	for _, info := range allSessions {
		if info.Status == session.Detached {
			detached = append(detached, info)
		}
	}

	var target *session.Session

	// inputCh is non-nil when the selector runs. A single goroutine reads
	// from sess into the channel so that BubbleTea's abandoned read loop
	// never steals bytes from the session.
	var inputCh <-chan []byte

	// inputDone is closed via defer so the sshInputChannel goroutine never
	// blocks forever on `ch <- data` after this handler returns — covers
	// every early-return path below.
	inputDone := make(chan struct{})
	defer close(inputDone)

	if len(detached) == 0 {
		// No detached sessions — create one directly.
		var err error
		target, err = mgr.Create(cols, rows, sshEnv)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                            //nolint:errcheck
			return
		}
	} else {
		// Start a goroutine that owns reading from the SSH channel.
		// BubbleTea reads from a closeable wrapper; the session IO
		// reads from the same channel afterward.
		inputCh = sshInputChannel(sess, inputDone)
		cr := &channelReader{ch: inputCh, done: make(chan struct{})}

		// Show selector with detached sessions only.
		screen := newSelectorScreen(detached)
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
		w := tui.NewWindow(header, screen)
		p := tea.NewProgram(w,
			tea.WithInput(cr),
			tea.WithOutput(sess),
			tea.WithEnvironment(sshEnv),
			tea.WithColorProfile(colorprofile.Env(sshEnv)),
			tea.WithWindowSize(int(cols), int(rows)),
			tea.WithoutSignalHandler(),
		)
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(sess.Stderr(), "selector: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                      //nolint:errcheck
			return
		}
		// Instantly unblock BubbleTea's abandoned read goroutine.
		close(cr.done)

		result := screen.result
		if result == nil {
			// User quit without selecting.
			sess.Exit(0) //nolint:errcheck
			return
		}
		if result.sessionID == "" {
			// New session.
			var err error
			target, err = mgr.Create(cols, rows, sshEnv)
			if err != nil {
				fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
				sess.Exit(1)                                            //nolint:errcheck
				return
			}
		} else {
			target = mgr.Get(result.sessionID)
			if target == nil {
				fmt.Fprintf(sess.Stderr(), "session %s not found\n", result.sessionID) //nolint:errcheck
				sess.Exit(1)                                                           //nolint:errcheck
				return
			}
		}
	}

	client := target.Attach(cols, rows)
	defer client.Close() //nolint:errcheck

	// Forward window resize (writer only).
	go func() {
		for win := range winCh {
			c, r := clampPTYSize(win.Width, win.Height)
			target.Resize(client, c, r)
		}
	}()

	// SSH keepalive: periodically send channel requests to detect dead
	// clients. SendRequest blocks until the peer replies, so we run it
	// in a goroutine with a timeout — a dead TCP connection can stall
	// the call indefinitely. When the timeout fires we close the
	// session client, which cascades through the handler and eventually
	// unblocks the stuck SendRequest.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reply := make(chan error, 1)
				go func() {
					_, err := sess.SendRequest(keepaliveRequestType, true, nil)
					reply <- err
				}()
				select {
				case err := <-reply:
					if err != nil {
						client.Close() //nolint:errcheck
						return
					}
				case <-time.After(3 * time.Second):
					client.Close() //nolint:errcheck
					return
				case <-sess.Context().Done():
					client.Close() //nolint:errcheck
					return
				}
			case <-sess.Context().Done():
				client.Close() //nolint:errcheck
				return
			}
		}
	}()

	// Copy SSH stdin to session. When the selector ran, a channel-based
	// reader already owns sess reads — consume from it to avoid two
	// goroutines reading from the same SSH channel.
	if inputCh != nil {
		go func() {
			for data := range inputCh {
				if _, err := client.Write(data); err != nil {
					break
				}
			}
		}()
	} else {
		go func() {
			io.Copy(client, sess) //nolint:errcheck
		}()
	}

	// Copy session output to SSH.
	done := make(chan struct{})
	go func() {
		io.Copy(sess, client) //nolint:errcheck
		close(done)
	}()

	<-done
	sess.Exit(0) //nolint:errcheck
}

func handleExecSession(sess charmssh.Session) {
	rawCmd := sess.RawCommand()
	if rawCmd == "" {
		fmt.Fprintln(sess.Stderr(), "no command specified") //nolint:errcheck
		sess.Exit(1)                                        //nolint:errcheck
		return
	}

	// Execute via the user's shell, matching OpenSSH sshd behavior.
	// The client shell-escapes individual arguments to preserve
	// boundaries (e.g. filenames with spaces), and the shell parses
	// them back.
	shell := userShell()
	cmd := exec.CommandContext(sess.Context(), shell, "-c", rawCmd)
	cmd.Env = session.MergeEnv(sess.Environ())
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()

	// Use StdinPipe instead of cmd.Stdin = sess. With cmd.Stdin,
	// cmd.Wait() blocks until the SSH channel sends EOF, which doesn't
	// happen until the client closes its side — deadlocking one-shot
	// commands like "uptime" that don't consume stdin. StdinPipe lets
	// cmd.Wait() close the pipe automatically after the process exits.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "stdin pipe: %s\n", err) //nolint:errcheck
		sess.Exit(1)                                        //nolint:errcheck
		return
	}
	go func() {
		io.Copy(stdinPipe, sess) //nolint:errcheck
		stdinPipe.Close()        //nolint:errcheck
	}()

	// Keep the connection alive during silent long-running commands. Without
	// this, the server-level IdleTimeout closes the conn → cmd context is
	// canceled → child is killed before it can finish.
	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go execKeepalive(sess, keepaliveDone)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			sess.Exit(exitErr.ExitCode()) //nolint:errcheck
			return
		}
		fmt.Fprintf(sess.Stderr(), "failed to run command: %s\n", err) //nolint:errcheck
		sess.Exit(1)                                                   //nolint:errcheck
		return
	}
	sess.Exit(0) //nolint:errcheck
}

// execKeepalive sends periodic SSH keepalive requests during a non-PTY exec
// session. The send is wantReply=false so it doesn't block; the write
// reaches the underlying TCP conn which resets the server-side idle
// deadline, preventing IdleTimeout from killing silent long-running
// commands. The goroutine exits when done is closed or when the session
// context is canceled (e.g. real client disconnect).
func execKeepalive(sess charmssh.Session, done <-chan struct{}) {
	ticker := time.NewTicker(execKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sess.SendRequest(keepaliveRequestType, false, nil) //nolint:errcheck
		case <-done:
			return
		case <-sess.Context().Done():
			return
		}
	}
}

// userShell returns the current user's login shell, falling back to /bin/sh.
func userShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// sshInputChannel starts a goroutine that reads from r and sends chunks
// on the returned channel. The channel is closed when r returns an error
// or when done is closed. The done channel lets callers unblock the
// goroutine if the consumer stops draining: without it, a full 16-slot
// buffer plus a stalled consumer would pin the goroutine forever on
// `ch <- data`.
func sshInputChannel(r io.Reader, done <-chan struct{}) <-chan []byte {
	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ch <- data:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// clampPTYSize normalizes client-supplied PTY dimensions to safe terminal
// bounds. Zero or negative values fall back to a sensible default; values
// above the cap are reduced to the cap. The cap protects the VTE buffer
// from unbounded width*height allocations driven by the SSH peer.
func clampPTYSize(cols, rows int) (uint16, uint16) {
	return clampDim(cols, defaultPTYCols, maxPTYCols), clampDim(rows, defaultPTYRows, maxPTYRows)
}

func clampDim(v int, dflt, limit uint16) uint16 {
	if v <= 0 {
		return dflt
	}
	if v > int(limit) {
		return limit
	}
	return uint16(v)
}

// channelReader wraps a byte channel as an io.Reader. Closing the done
// channel immediately unblocks any pending Read with io.EOF.
type channelReader struct {
	ch   <-chan []byte
	buf  []byte
	done chan struct{}
}

func (r *channelReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		select {
		case data, ok := <-r.ch:
			if !ok {
				return 0, io.EOF
			}
			r.buf = data
		case <-r.done:
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
