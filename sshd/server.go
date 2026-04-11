package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
		Handler: s.handleSession,
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
	cols := uint16(ptyReq.Window.Width)
	rows := uint16(ptyReq.Window.Height)
	mgr := s.sessions

	// Build environment from SSH session (includes TERM, etc.).
	// SSH clients typically don't forward COLORTERM. Fall back to the
	// container's own value so the TUI can detect TrueColor support.
	sshEnv := sess.Environ()
	sshEnv = append(sshEnv, fmt.Sprintf("TERM=%s", ptyReq.Term))
	if !hasEnvPrefix(sshEnv, "COLORTERM=") {
		if ct := os.Getenv("COLORTERM"); ct != "" {
			sshEnv = append(sshEnv, "COLORTERM="+ct)
		}
	}

	allSessions := mgr.List()

	// Only detached sessions are relevant for the selector.
	var detached []session.SessionInfo
	for _, info := range allSessions {
		if info.Status == "detached" {
			detached = append(detached, info)
		}
	}

	var target *session.Session

	// inputCh is non-nil when the selector runs. A single goroutine reads
	// from sess into the channel so that BubbleTea's abandoned read loop
	// never steals bytes from the session.
	var inputCh <-chan []byte

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
		inputCh = sshInputChannel(sess)
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
			target.Resize(client, uint16(win.Width), uint16(win.Height))
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
					_, err := sess.SendRequest("keepalive@openssh.com", true, nil)
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

// hasEnvPrefix reports whether any entry in env starts with prefix.
func hasEnvPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// userShell returns the current user's login shell, falling back to /bin/sh.
func userShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// sshInputChannel starts a goroutine that reads from r and sends chunks
// on the returned channel. The channel is closed when r returns an error.
func sshInputChannel(r io.Reader) <-chan []byte {
	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				ch <- data
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
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
