package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/session"

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
	sshEnv := sess.Environ()
	sshEnv = append(sshEnv, fmt.Sprintf("TERM=%s", ptyReq.Term))

	sessions := mgr.List()

	var target *session.Session
	var takeOver bool

	if len(sessions) == 0 {
		// No sessions — create one directly.
		var err error
		target, err = mgr.Create(cols, rows, sshEnv)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                            //nolint:errcheck
			return
		}
	} else {
		// Show selector.
		model := newSelectorModel(sessions)
		p := tea.NewProgram(model,
			tea.WithInput(sess),
			tea.WithOutput(sess),
			tea.WithWindowSize(int(cols), int(rows)),
			tea.WithoutSignalHandler(),
		)
		finalModel, err := p.Run()
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "selector: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                      //nolint:errcheck
			return
		}
		result := finalModel.(selectorModel).result
		if result == nil {
			// User quit without selecting.
			sess.Exit(0) //nolint:errcheck
			return
		}
		if result.sessionID == "" {
			// New session.
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
			takeOver = result.takeOver
		}
	}

	client := target.Attach(cols, rows)
	defer client.Close() //nolint:errcheck

	if takeOver {
		target.TakeOver(client)
	}

	// Forward window resize (writer only).
	go func() {
		for win := range winCh {
			target.Resize(client, uint16(win.Width), uint16(win.Height))
		}
	}()

	// Copy SSH stdin to session.
	go func() {
		io.Copy(client, sess) //nolint:errcheck
	}()

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
	args := sess.Command()
	if len(args) == 0 {
		fmt.Fprintln(sess.Stderr(), "no command specified") //nolint:errcheck
		sess.Exit(1)                                        //nolint:errcheck
		return
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = session.MergeEnv(sess.Environ())
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	cmd.Stdin = sess

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
