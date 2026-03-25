package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// shellescape quotes a string for safe transmission over SSH wire protocol.
func shellescape(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, c := range s {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			c != '-' && c != '_' && c != '.' && c != '/' && c != ':' && c != ',' && c != '+' && c != '=' {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func SSHCommand() *cli.Command {
	return &cli.Command{
		Name:   "ssh",
		Usage:  "Connect to running sandbox via SSH",
		Action: SSHAction,
	}
}

func SSHAction(ctx context.Context, cmd *cli.Command) error {
	client, err := ctr.NewClient(ctr.WithDebug(cmd.Root().Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close()

	// Resolve project root from working directory.
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectRoot, err := filepath.Abs(wd)
	if err != nil {
		return err
	}
	projectRoot, err = config.FindProjectRoot(projectRoot)
	if err != nil {
		return err
	}

	// Discover sandbox.
	sandboxID, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if sandboxID == "" {
		return fmt.Errorf("no running sandbox found — run 'vibepit up' first")
	}

	// Get session ID and published SSH port.
	sessionID, err := client.SessionIDFromContainer(ctx, sandboxID)
	if err != nil {
		return err
	}
	port, err := client.FindPublishedPort(ctx, sandboxID, "2222/tcp")
	if err != nil {
		return err
	}

	// Load credentials from session dir.
	sessDir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	privateKey, err := os.ReadFile(filepath.Join(sessDir, "ssh-key"))
	if err != nil {
		return fmt.Errorf("read ssh key: %w (credentials missing — run 'vibepit down && vibepit up')", err)
	}
	hostPubKey, err := os.ReadFile(filepath.Join(sessDir, "host-key.pub"))
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	// Parse keys.
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPubKey)
	if err != nil {
		return fmt.Errorf("parse host key: %w", err)
	}

	// Connect.
	conn, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &ssh.ClientConfig{
		User:            "code",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
	})
	if err != nil {
		return fmt.Errorf("ssh connect: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close() //nolint:errcheck

	// Command mode — argv semantics via shell-quoting.
	cmdArgs := cmd.Args().Slice()
	if len(cmdArgs) > 0 {
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		session.Stdin = os.Stdin
		quoted := make([]string, len(cmdArgs))
		for i, arg := range cmdArgs {
			quoted[i] = shellescape(arg)
		}
		if err := session.Run(strings.Join(quoted, " ")); err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				return &ctr.ExitError{Code: exitErr.ExitStatus()}
			}
			return err
		}
		return nil
	}

	// Interactive mode.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState) //nolint:errcheck

	w, h, err := term.GetSize(fd)
	if err != nil {
		w, h = 80, 24
	}
	termEnv := os.Getenv("TERM")
	if termEnv == "" {
		termEnv = "xterm-256color"
	}

	if err := session.RequestPty(termEnv, h, w, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	session.Stdin = os.Stdin

	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	// Forward SIGWINCH.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()
	go func() {
		for range sigCh {
			if w, h, err := term.GetSize(fd); err == nil {
				session.WindowChange(h, w) //nolint:errcheck
			}
		}
	}()

	return session.Wait()
}
