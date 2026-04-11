package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func findProxyForSession(ctx context.Context, client *ctr.Client, sessionID string) (string, error) {
	containers, err := client.SessionContainers(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		if c.Role == ctr.RoleProxy {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no proxy container found for session %s", sessionID)
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

	// Always resolve project root from cwd — all positional args are the
	// remote command, not a project path.
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectRoot, err := config.FindProjectRoot(wd)
	if err != nil {
		return err
	}

	sandbox, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if sandbox == nil {
		return fmt.Errorf("no running sandbox found — run 'vibepit up' first")
	}

	// SSH port is published on the proxy container (forwarded to sandbox).
	proxyID, err := findProxyForSession(ctx, client, sandbox.SessionID)
	if err != nil {
		return err
	}
	port, err := client.FindPublishedPort(ctx, proxyID, ctr.SSHContainerPort)
	if err != nil {
		return fmt.Errorf("find SSH port: %w", err)
	}

	sessDir, err := sessionDir(sandbox.SessionID)
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

	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPubKey)
	if err != nil {
		return fmt.Errorf("parse host key: %w", err)
	}

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

	// Command mode — join args with spaces, matching ssh(1) behavior.
	// The server executes via the user's shell, so the shell handles
	// parsing. No client-side escaping needed.
	cmdArgs := cmd.Args().Slice()
	if len(cmdArgs) > 0 {
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr

		// Use StdinPipe so the remote command can read from our stdin
		// (piped or terminal) without blocking session.Wait() after the
		// command exits. Wait() only waits for stdout/stderr completion;
		// the stdin copy goroutine is interrupted when the session closes.
		stdinPipe, err := session.StdinPipe()
		if err != nil {
			return fmt.Errorf("stdin pipe: %w", err)
		}
		go func() {
			io.Copy(stdinPipe, os.Stdin) //nolint:errcheck
			stdinPipe.Close()            //nolint:errcheck
		}()

		if err := session.Run(strings.Join(cmdArgs, " ")); err != nil {
			if exitErr, ok := errors.AsType[*ssh.ExitError](err); ok {
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
