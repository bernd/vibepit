package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
	gossh "golang.org/x/crypto/ssh"

	charmssh "github.com/charmbracelet/ssh"
)

// Config holds configuration for the SSH server.
type Config struct {
	HostKeyPEM    []byte
	AuthorizedKey []byte
}

// Server wraps a charmbracelet/ssh server with public key authentication.
type Server struct {
	server *charmssh.Server
}

// NewServer creates a new SSH server that authenticates using the given
// authorized key and presents the given host key.
func NewServer(cfg Config) (*Server, error) {
	authorizedKey, _, _, _, err := gossh.ParseAuthorizedKey(cfg.AuthorizedKey)
	if err != nil {
		return nil, fmt.Errorf("parse authorized key: %w", err)
	}

	srv := &charmssh.Server{
		Handler: handleSession,
	}

	if err := srv.SetOption(charmssh.HostKeyPEM(cfg.HostKeyPEM)); err != nil {
		return nil, fmt.Errorf("set host key: %w", err)
	}

	if err := srv.SetOption(charmssh.PublicKeyAuth(func(_ charmssh.Context, key charmssh.PublicKey) bool {
		return charmssh.KeysEqual(key, authorizedKey)
	})); err != nil {
		return nil, fmt.Errorf("set public key auth: %w", err)
	}

	return &Server{server: srv}, nil
}

// Serve accepts incoming connections on the listener.
func (s *Server) Serve(l net.Listener) error {
	return s.server.Serve(l)
}

// Close immediately closes all active listeners and connections.
func (s *Server) Close() error {
	return s.server.Close()
}

func handleSession(sess charmssh.Session) {
	ptyReq, winCh, isPty := sess.Pty()
	if isPty {
		handlePTYSession(sess, ptyReq, winCh)
	} else {
		handleExecSession(sess)
	}
}

// mergeEnv returns the container's environment with session-provided vars overlaid.
func mergeEnv(sessionEnv []string) []string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	for _, e := range sessionEnv {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func handlePTYSession(sess charmssh.Session, ptyReq charmssh.Pty, winCh <-chan charmssh.Window) {
	cmd := exec.Command("/bin/bash", "--login")
	cmd.Env = mergeEnv(sess.Environ())
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(ptyReq.Window.Height),
		Cols: uint16(ptyReq.Window.Width),
	})
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "failed to start shell: %s\n", err)
		sess.Exit(1) //nolint:errcheck
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for win := range winCh {
			pty.Setsize(ptmx, &pty.Winsize{ //nolint:errcheck
				Rows: uint16(win.Height),
				Cols: uint16(win.Width),
			})
		}
	})
	wg.Go(func() {
		io.Copy(ptmx, sess) //nolint:errcheck
	})
	wg.Go(func() {
		io.Copy(sess, ptmx) //nolint:errcheck
	})

	if exitErr, ok := errors.AsType[*exec.ExitError](cmd.Wait()); ok {
		sess.Exit(exitErr.ExitCode()) //nolint:errcheck
		ptmx.Close()
		wg.Wait()
		return
	}
	ptmx.Close()
	wg.Wait()
	sess.Exit(0) //nolint:errcheck
}

func handleExecSession(sess charmssh.Session) {
	args := sess.Command()
	if len(args) == 0 {
		fmt.Fprintln(sess.Stderr(), "no command specified")
		sess.Exit(1) //nolint:errcheck
		return
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = mergeEnv(sess.Environ())
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	cmd.Stdin = sess

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			sess.Exit(exitErr.ExitCode()) //nolint:errcheck
			return
		}
		fmt.Fprintf(sess.Stderr(), "failed to run command: %s\n", err)
		sess.Exit(1) //nolint:errcheck
		return
	}
	sess.Exit(0) //nolint:errcheck
}
