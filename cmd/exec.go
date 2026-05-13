package cmd

import (
	"context"
	"errors"
	"fmt"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/sshd"
	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"strings"
)

func ExecCommand() *cli.Command {
	return &cli.Command{
		Name:  "exec",
		Usage: "Execute command in the sandbox",
		// All args after "exec" are the remote command and may contain
		// dashes (e.g. "vibepit exec cat -e -"). If we add flags to
		// this subcommand, replace this with manual arg parsing or a
		// "--" separator.
		SkipFlagParsing: true,
		Action:          ExecAction,
	}
}

func ExecAction(ctx context.Context, cmd *cli.Command) error {
	conn, _, err := newSSHClient(ctx, cmd.Root().Bool(debugFlag))
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close() //nolint:errcheck

	// Command mode — shell-escape each argument before joining so
	// spaces, quotes, $VAR, $(cmd), and globs survive the wire as
	// literals. The server runs the result via `shell -c`, matching
	// OpenSSH exec semantics.
	cmdArgs := cmd.Args().Slice()
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

	if err := session.Run(buildRemoteCommand(cmdArgs)); err != nil {
		if exitErr, ok := errors.AsType[*ssh.ExitError](err); ok {
			return &ctr.ExitError{Code: exitErr.ExitStatus()}
		}
		return err
	}
	return nil
}

// buildRemoteCommand turns an argument vector into a single shell-safe
// command line for the remote side's "shell -c" invocation. Each argument
// is shell-escaped so metacharacters (spaces, quotes, $, globs) survive
// the round trip as literals instead of being re-parsed by the remote
// shell. Matches the contract documented on the server side in
// sshd.handleExecSession.
func buildRemoteCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	escaped := make([]string, len(args))
	for i, a := range args {
		escaped[i] = sshd.ShellEscape(a)
	}
	return strings.Join(escaped, " ")
}
