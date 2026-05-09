package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/sshd"
	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func SSHCommand() *cli.Command {
	return &cli.Command{
		Name:  "ssh",
		Usage: "Connect to running sandbox via SSH",
		// All args after "ssh" are the remote command and may contain
		// dashes (e.g. "vibepit ssh cat -e -"). If we add flags to
		// this subcommand, replace this with manual arg parsing or a
		// "--" separator.
		SkipFlagParsing: true,
		Action:          SSHAction,
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
	proxyID, err := client.FindProxyContainerID(ctx, sandbox.SessionID)
	if err != nil {
		return err
	}
	port, err := client.FindPublishedPort(ctx, proxyID, ctr.SSHContainerPort)
	if err != nil {
		return fmt.Errorf("find SSH port: %w", err)
	}

	sessDir := sessionDir(sandbox.SessionID)
	privateKey, err := os.ReadFile(filepath.Join(sessDir, SSHClientPrivFile))
	if err != nil {
		return fmt.Errorf("read ssh key: %w (credentials missing — run 'vibepit down && vibepit up')", err)
	}
	hostPubKey, err := os.ReadFile(filepath.Join(sessDir, SSHHostPubFile))
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

	// Command mode — shell-escape each argument before joining so
	// spaces, quotes, $VAR, $(cmd), and globs survive the wire as
	// literals. The server runs the result via `shell -c`, matching
	// OpenSSH exec semantics.
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

		if err := session.Run(buildRemoteCommand(cmdArgs)); err != nil {
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
	var restoreOnce sync.Once
	restoreTerminal := func() {
		restoreOnce.Do(func() { term.Restore(fd, oldState) }) //nolint:errcheck
	}
	defer restoreTerminal()

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

	// Use StdinPipe + a channel-based reader instead of session.Stdin.
	// Setting session.Stdin makes the SSH library start a goroutine that
	// reads from os.Stdin. After session.Wait(), that goroutine stays
	// alive (blocked in Read), racing with the shutdown prompt for user
	// input. Instead, one goroutine owns os.Stdin reads and sends to a
	// channel; a stoppable copy goroutine routes the channel to the SSH
	// pipe. After the session ends, we stop the copy goroutine and
	// redirect the channel to the prompt reader.
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdinCh := make(chan []byte, 16)
	go func() {
		defer close(stdinCh)
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				stdinCh <- data
			}
			if err != nil {
				return
			}
		}
	}()
	stopCopy := make(chan struct{})
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		defer stdinPipe.Close() //nolint:errcheck
		for {
			select {
			case data, ok := <-stdinCh:
				if !ok {
					return
				}
				if _, err := stdinPipe.Write(data); err != nil {
					return
				}
			case <-stopCopy:
				return
			}
		}
	}()

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

	waitErr := session.Wait()

	// Stop the copy goroutine and wait for it to exit. After this,
	// stdinCh has no consumer, so the prompt reader can take over.
	close(stopCopy)
	<-copyDone

	restoreTerminal()

	if waitErr != nil {
		return waitErr
	}

	return handleLastExit(handleLastExitParams{
		transport:  conn,
		stdin:      &channelStdinReader{ch: stdinCh},
		stderr:     os.Stderr,
		isTerminal: term.IsTerminal(fd),
		shutdownFn: func() error {
			return DownAction(ctx, cmd)
		},
	})
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

type sessionCountTransport interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
}

type handleLastExitParams struct {
	transport  sessionCountTransport
	stdin      io.Reader
	stderr     io.Writer
	isTerminal bool
	shutdownFn func() error
}

func handleLastExit(p handleLastExitParams) error {
	if !p.isTerminal {
		return nil
	}

	ok, payload, err := p.transport.SendRequest("session-count@vibepit", true, nil)
	p.transport.Close() //nolint:errcheck
	if err != nil || !ok {
		return nil //nolint:nilerr // silent exit per spec: old daemon or transport error
	}

	var reply sshd.SessionCountReply
	if err := ssh.Unmarshal(payload, &reply); err != nil {
		return nil //nolint:nilerr // silent exit per spec: malformed reply
	}

	if reply.PTYConns > 0 || reply.ExecCount > 0 {
		return nil
	}

	fmt.Fprintln(p.stderr, "You were the last connection.")
	if reply.DetachedPTY > 0 && reply.DetachedInfo != "" {
		fmt.Fprintf(p.stderr, "%d detached session(s) will be killed:\n", reply.DetachedPTY)
		for line := range strings.SplitSeq(reply.DetachedInfo, "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) == 3 {
				fmt.Fprintf(p.stderr, "  %-12s %-8s %s\n", parts[0], parts[1], parts[2])
			}
		}
	}
	fmt.Fprint(p.stderr, "Shut down the sandbox? [y/N] ")

	reader := bufio.NewReader(p.stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return nil //nolint:nilerr // stdin EOF or read error treated as "no"
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "y" || answer == "yes" {
		return p.shutdownFn()
	}
	return nil
}

// channelStdinReader adapts a byte channel as an io.Reader. Used to
// redirect stdin from the SSH copy goroutine to the shutdown prompt
// without competing readers on os.Stdin.
type channelStdinReader struct {
	ch  <-chan []byte
	buf []byte
}

func (r *channelStdinReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		data, ok := <-r.ch
		if !ok {
			return 0, io.EOF
		}
		r.buf = data
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
