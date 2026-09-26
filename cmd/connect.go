package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/sshd"
	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func ConnectCommand() *cli.Command {
	return &cli.Command{
		Name:    "connect",
		Aliases: []string{"c"},
		Usage:   "Connect to the running sandbox",
		Flags:   []cli.Flag{promptCLIFlag},
		Action:  ConnectAction,
	}
}

func ConnectAction(ctx context.Context, cmd *cli.Command) error {
	conn, info, err := newSSHClient(ctx, cmd.Root().Bool(debugFlag), cmd.Bool(promptFlag))
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck

	prompter, err := startBlockPrompter(ctx, cmd, func() (*SessionInfo, error) { return info, nil })
	if err != nil {
		return err
	}
	defer prompter.stop()

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("connect session: %w", err)
	}
	defer session.Close() //nolint:errcheck

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

	size := func() (int, int, error) { return term.GetSize(fd) }
	w, h, err := size()
	if err != nil {
		w, h = 80, 24
	}
	termEnv := containerTerm()

	if err := session.RequestPty(termEnv, h, w, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}

	// One goroutine owns os.Stdin reads for the whole command. It can't be
	// stopped, so the session and the shutdown prompt below take turns on
	// its channel through a stdinHandoff; runSSHTerminal switches it over.
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
	stdin := newStdinHandoff(stdinCh)

	winch := make(chan os.Signal, 1)
	ctr.NotifyResize(winch)
	defer signal.Stop(winch)

	waitErr := runSSHTerminal(ctx, sshTerminalParams{
		session:  session,
		stdin:    stdin,
		stdout:   os.Stdout,
		size:     size,
		winch:    winch,
		prompter: prompter,
	})
	// Stop before the shutdown prompt: it may take the proxy down.
	prompter.stop()
	restoreTerminal()
	prompter.report()

	if waitErr != nil {
		// A forced detach (keepalive timeout across a suspend/resume, lost
		// connection) surfaces as the dedicated disconnect exit code. The
		// shell itself is still running, so explain how to get back instead
		// of leaking a bare "Process exited with status 255".
		if isSandboxDisconnect(waitErr) {
			fmt.Fprintln(os.Stderr, "\nDisconnected from the sandbox. Your session is still running — reconnect with 'vibepit connect'.")
			return nil
		}
		return waitErr
	}

	return handleLastExit(handleLastExitParams{
		transport:  conn,
		stdin:      stdin.Prompt(),
		stderr:     os.Stderr,
		isTerminal: term.IsTerminal(fd),
		shutdownFn: func() error {
			return DownAction(ctx, cmd)
		},
	})
}

type sshTerminalParams struct {
	session  *ssh.Session // with a PTY requested, before Shell
	stdin    *stdinHandoff
	stdout   io.Writer
	size     func() (cols, rows int, err error)
	winch    <-chan os.Signal // local terminal resizes
	prompter *blockPrompter
}

// runSSHTerminal starts the shell and forwards it through an
// overlay.Terminal, so the prompter can show prompts over it. It returns
// once the session ended, with session.Wait's error, or ctx's error. By
// then stdin has been stopped.
func runSSHTerminal(ctx context.Context, p sshTerminalParams) error {
	sess := p.session
	stdinPipe, err := sess.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	// vibed writes its own diagnostics to stderr even with a PTY. Both
	// streams go through the overlay, so nothing lands on a prompt.
	outR, outW := io.Pipe()
	sess.Stdout = outW
	sess.Stderr = outW

	if err := sess.Shell(); err != nil {
		p.stdin.Stop()
		return fmt.Errorf("start shell: %w", err)
	}
	// Create the Terminal only once the shell runs: only Run releases it,
	// and a prompt needs Run. The pipe holds the shell's output until then.
	t := overlay.New(overlay.Config{
		Stdin:      p.stdin.Session(),
		Stdout:     p.stdout,
		SessionIn:  stdinPipe,
		SessionOut: outR,
		CloseInput: stdinPipe.Close,
		Resize:     func(cols, rows int) { sess.WindowChange(rows, cols) }, //nolint:errcheck
		Size:       p.size,
		Logf:       p.prompter.logf,
		NoShadow:   p.prompter.onTerminal == nil,
	})
	if p.prompter.onTerminal != nil {
		p.prompter.onTerminal(t)
	}

	waitCh := make(chan error, 1)
	go func() {
		err := sess.Wait()
		// Input from here on is for the shutdown prompt, not the ended
		// session.
		p.stdin.Stop()
		outW.Close() //nolint:errcheck
		waitCh <- err
	}()

	done := make(chan struct{})
	defer close(done)
	go ctr.WatchResizeSignals(p.winch, done, t.Resize)

	if err := t.Run(ctx); err != nil {
		// Run only ends early when ctx is done. Don't wait for the server
		// to confirm the close.
		p.stdin.Stop()
		outR.Close() //nolint:errcheck
		sess.Close() //nolint:errcheck
		return err
	}
	return <-waitCh
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

// isSandboxDisconnect reports whether a session.Wait error is the server's
// signal that the client was force-detached (keepalive/lost connection) rather
// than the shell exiting. The server reports DisconnectExitCode in that case;
// a genuine shell exit always reports 0, so the code is unambiguous. Matching
// on the ExitStatus interface (rather than *ssh.ExitError directly) keeps the
// check testable without constructing an unexported ssh error.
func isSandboxDisconnect(err error) bool {
	var exitErr interface{ ExitStatus() int }
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus() == sshd.DisconnectExitCode
	}
	return false
}

// stdinHandoff passes the local terminal's input, read into ch by one
// goroutine, first to the session and then, after Stop, to the shutdown
// prompt. A chunk the session's reader holds unread at Stop goes to the
// prompt; input it already returned stays with the session.
type stdinHandoff struct {
	ch       <-chan []byte
	stop     chan struct{}
	stopOnce sync.Once

	mu   sync.Mutex // held for a whole session read, so Stop can wait one out
	left []byte     // for the prompt
}

func newStdinHandoff(ch <-chan []byte) *stdinHandoff {
	return &stdinHandoff{ch: ch, stop: make(chan struct{})}
}

// Session reads input until Stop, then returns io.EOF.
func (h *stdinHandoff) Session() io.Reader { return handoffSession{h} }

// Stop ends the session's reads and waits for a read in progress to return.
func (h *stdinHandoff) Stop() {
	h.stopOnce.Do(func() { close(h.stop) })
	h.mu.Lock()
	h.mu.Unlock() //nolint:staticcheck // waits for a session read to finish
}

// Prompt reads input after Stop.
func (h *stdinHandoff) Prompt() io.Reader {
	h.mu.Lock()
	defer h.mu.Unlock()
	left := h.left
	h.left = nil
	return io.MultiReader(bytes.NewReader(left), &channelStdinReader{ch: h.ch})
}

type handoffSession struct{ h *stdinHandoff }

func (s handoffSession) Read(p []byte) (int, error) {
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	for len(h.left) == 0 {
		select {
		case <-h.stop:
			return 0, io.EOF
		default:
		}
		select {
		case data, ok := <-h.ch:
			if !ok {
				return 0, io.EOF
			}
			h.left = data
		case <-h.stop:
			return 0, io.EOF
		}
	}
	select {
	case <-h.stop:
		// The session ended while this read waited: the input is the
		// prompt's.
		return 0, io.EOF
	default:
	}
	n := copy(p, h.left)
	h.left = h.left[n:]
	return n, nil
}

// channelStdinReader adapts a byte channel as an io.Reader.
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
