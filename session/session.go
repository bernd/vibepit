package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	vt "github.com/unixshells/vt-go"
)

// attachReplayTestHook is a no-op in production; tests override it to
// widen the race window between s.clients append and replay delivery.
var attachReplayTestHook = func() {}

// Session represents a persistent PTY shell session that survives client
// disconnects. It manages the shell process, PTY, attached clients, and
// output fan-out.
type Session struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	sid       int
	createdAt time.Time
	manager   *Manager

	mu               sync.Mutex
	clients          []*Client
	writer           *Client
	exited           bool
	exitCode         int
	exitedAt         time.Time
	detachedAt       time.Time
	lastDetachReason DetachReason
	vte              *vt.SafeEmulator
	cols             uint16
	rows             uint16

	pumpDone    chan struct{} // closed when pump goroutine exits; barrier for vte.Close
	drainDone   chan struct{} // closed by drainVTE on exit; awaited before vte.Close so no reader races with close
	exitDone    chan struct{} // closed when waitForExit fully returns, after the final onSessionChanged/state-file write
	cleanup     chan struct{}
	cleanupOnce sync.Once
}

func newSession(id string, cols, rows uint16, env []string, mgr *Manager) (*Session, error) {
	shellCmd := []string{"/bin/bash", "--login"}
	if mgr != nil && len(mgr.Command) > 0 {
		shellCmd = mgr.Command
	}
	cmd := exec.Command(shellCmd[0], shellCmd[1:]...)
	cmd.Env = MergeEnv(env)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Session{
		id:        id,
		cmd:       cmd,
		ptmx:      ptmx,
		sid:       cmd.Process.Pid,
		createdAt: time.Now(),
		manager:   mgr,
		vte:       vt.NewSafeEmulator(int(cols), int(rows)),
		cols:      cols,
		rows:      rows,
		pumpDone:  make(chan struct{}),
		drainDone: make(chan struct{}),
		exitDone:  make(chan struct{}),
		cleanup:   make(chan struct{}),
	}

	go s.pump()
	go s.drainVTE()
	go s.waitForExit()

	return s, nil
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.id }

// Exited returns true if the shell process has exited.
func (s *Session) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// Info returns a snapshot of the session's current state.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := SessionInfo{
		ID:               s.id,
		Command:          s.cmd.Path,
		ClientCount:      len(s.clients),
		CreatedAt:        s.createdAt,
		ExitCode:         s.exitCode,
		ExitedAt:         s.exitedAt,
		DetachedAt:       s.detachedAt,
		LastDetachReason: s.lastDetachReason,
	}
	if s.exited {
		info.Status = Exited
	} else if len(s.clients) > 0 {
		info.Status = Attached
	} else {
		info.Status = Detached
	}
	return info
}

// Attach creates a new client and attaches it to this session. The first
// client to attach becomes the writer. If cols/rows are provided and this
// client becomes the writer, the PTY is resized.
//
// On attach, the client receives a replay of scrollback history plus the
// current VTE screen state so the terminal appears restored.
func (s *Session) Attach(cols, rows uint16) *Client {
	s.mu.Lock()

	// If the session has already exited, return a client with its output
	// pre-closed so the caller sees clean EOF. Do not add to s.clients —
	// nothing will ever drive it.
	if s.exited {
		c := newClient(s)
		s.mu.Unlock()
		c.closeOutput()
		return c
	}

	// Snapshot state while holding the lock.
	altScreen := s.vte.IsAltScreen()
	vteScreen := s.vte.Render()
	cursorPos := s.vte.CursorPosition()
	// Render VTE scrollback (only truly off-screen lines) via the per-cell
	// safe accessor.
	scrollbackData := renderVTEScrollback(s.vte)

	c := newClient(s)

	becameWriter := false
	if s.writer == nil {
		s.writer = c
		becameWriter = true
		if cols > 0 && rows > 0 && (cols != s.cols || rows != s.rows) {
			s.resizeLocked(cols, rows)
		}
	}

	// Deliver replay BEFORE appending c to s.clients, all under s.mu so
	// pump can't fan out to c between the VTE snapshot and c becoming
	// visible. Delivery is non-blocking for a fresh client (empty 1024-slot
	// channel), so holding s.mu here can't deadlock on the channel-full
	// Close path.
	if altScreen {
		// Alt-screen app (vim, less, htop): the PTY is already in
		// alt-screen, but the new SSH channel isn't — switch the client
		// into it and clear the screen.
		c.deliver([]byte("\033[?1049h\033[2J"))
	} else {
		// Normal mode or non-alt-screen TUI (Claude Code, Codex, shell):
		// replay scrollback history + VTE screen state + cursor position.
		// Scrollback lines scroll off naturally as the VTE screen fills
		// the visible area, populating the client's scroll buffer.
		var replay []byte
		replay = append(replay, "\033c"...) // terminal reset
		if len(scrollbackData) > 0 {
			replay = append(replay, scrollbackData...)
		}
		replay = append(replay, vteScreen...)
		if len(vteScreen) > 0 {
			// CSI cursor-position (ESC[<row>;<col>H) - 1-indexed; cursorPos is 0-indexed.
			replay = append(replay, fmt.Sprintf("\033[%d;%dH", cursorPos.Y+1, cursorPos.X+1)...)
		}
		// The replay is reconstructed screen content (scrollback + VTE render)
		// whose line breaks are bare \n. The connected terminal is in raw mode,
		// so each line must return to column 0 explicitly — emit CRLF. Live PTY
		// output is deliberately not rewritten (a raw-mode TUI's bare \n means
		// "down, keep column" and must reach the client intact); only this
		// vibed-generated replay needs newline semantics.
		replay = ToCRLF(replay)
		if len(replay) > 0 {
			c.deliver(replay)
		}
	}

	attachReplayTestHook()

	s.clients = append(s.clients, c)

	s.mu.Unlock()

	if s.manager != nil {
		go s.manager.onSessionChanged()
	}

	// Only writer clients may write to the PTY. The Ctrl-L nudge triggers
	// a full redraw of TUI apps; SIGWINCH only triggers partial redraws
	// in some apps, so Ctrl-L is the reliable "redraw screen" command.
	if altScreen && becameWriter {
		s.ptmx.Write([]byte{0x0c}) //nolint:errcheck
	}

	// SIGWINCH is harmless for shells and triggers a redraw for TUI apps.
	if s.cmd.Process != nil {
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGWINCH) //nolint:errcheck
	}

	return c
}

// TakeOver promotes the given client to writer, replacing the current writer.
// If cols/rows are non-zero, the PTY and VTE are resized to match the new
// writer's terminal dimensions.
func (s *Session) TakeOver(c *Client, cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer = c
	if cols > 0 && rows > 0 && (cols != s.cols || rows != s.rows) {
		s.resizeLocked(cols, rows)
	}
}

// resizeLocked updates the PTY and VTE to cols×rows and records the new
// dimensions on the session. Must be called with s.mu held.
func (s *Session) resizeLocked(cols, rows uint16) {
	pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
	s.vte.Resize(int(cols), int(rows))
	s.cols = cols
	s.rows = rows
}

// Detach removes a client from the session. If the detached client was the
// writer, the most recently attached remaining client is promoted.
func (s *Session) Detach(c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, cl := range s.clients {
		if cl == c {
			s.clients = append(s.clients[:i], s.clients[i+1:]...)
			break
		}
	}
	if s.writer == c {
		s.writer = nil
		if len(s.clients) > 0 {
			s.writer = s.clients[len(s.clients)-1]
		}
	}
	if len(s.clients) == 0 {
		s.detachedAt = time.Now()
	}
	// Record why the client left so status/monitoring can distinguish an
	// ordinary disconnect from a keepalive-driven drop. A genuine shell exit
	// closes the output channel rather than calling Close, so it never reaches
	// here with a reason.
	if r := c.DetachReason(); r != DetachNone {
		s.lastDetachReason = r
	}
	// If this was the last client and the session has exited, start the
	// tombstone expiry timer. This handles the normal case where the user
	// types "exit" while attached — waitForExit sees clients > 0 and skips
	// the timer, so we start it here when the last client detaches.
	if len(s.clients) == 0 && s.exited {
		go s.expireTombstone()
	}
	if s.manager != nil {
		go s.manager.onSessionChanged()
	}
}

// Resize changes the PTY dimensions. Only the writer client may resize.
// Resizes that don't change the dimensions are dropped to avoid redundant
// ioctls and VTE allocations from a chatty client.
func (s *Session) Resize(c *Client, cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer != c {
		return
	}
	if cols == s.cols && rows == s.rows {
		return
	}
	s.resizeLocked(cols, rows)
}

// WriteInput sends input to the PTY. Only the writer client may write.
func (s *Session) WriteInput(c *Client, p []byte) (int, error) {
	s.mu.Lock()
	if s.writer != c {
		s.mu.Unlock()
		return 0, fmt.Errorf("not the writer")
	}
	s.mu.Unlock()
	return s.ptmx.Write(p)
}

// drainVTE continuously reads from the VTE's internal response pipe. The VTE
// generates responses for terminal queries (DA, DSR, cursor position, etc.).
// If this pipe isn't drained, Write() blocks when the buffer fills up.
// drainVTE closes drainDone when it exits. waitForExit first closes the VTE's
// response pipe to unblock this read, then waits on drainDone before closing
// the emulator itself.
func (s *Session) drainVTE() {
	defer close(s.drainDone)
	buf := make([]byte, 1024)
	for {
		if _, err := s.vte.Read(buf); err != nil {
			return
		}
	}
}

// pump reads PTY output, feeds it into the VTE emulator under s.mu, and
// fans out to all attached clients. On PTY read error (shell exit or
// ptmx.Close from waitForExit), it sets s.exited = true atomically with
// the final client snapshot and closes every client's output channel.
// Setting s.exited inside the error-path critical section is what makes
// Attach's s.exited guard race-free: any Attach that holds s.mu after
// pump's error path has run sees the flag; any that ran before is
// included in pump's snapshot.
func (s *Session) pump() {
	defer close(s.pumpDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.Lock()
			s.vte.Write(data) //nolint:errcheck
			clients := append([]*Client(nil), s.clients...)
			s.mu.Unlock()

			for _, c := range clients {
				c.deliver(data)
			}
		}
		if err != nil {
			s.mu.Lock()
			s.exited = true
			clients := append([]*Client(nil), s.clients...)
			s.mu.Unlock()

			for _, c := range clients {
				c.closeOutput()
			}
			return
		}
	}
}

// waitForExit waits for the shell process to exit, cleans up descendant
// processes, tears down the PTY and VTE in an order that avoids racing
// pump's final write, and records exit metadata. s.exited is set here
// early (before descendant cleanup) to reject new attaches; pump sets it
// again idempotently in its error-path critical section.
func (s *Session) waitForExit() {
	// exitDone signals that the entire exit-handling sequence — including the
	// final onSessionChanged/state-file write below — has completed. Teardown
	// (e.g. test cleanup) waits on this rather than s.exited, which is set
	// early and does not cover the trailing state-file write.
	defer close(s.exitDone)

	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	s.mu.Lock()
	s.exited = true
	s.mu.Unlock()

	cleanupDescendants(s.cmd.Process.Pid, s.sid)

	// Close the PTY; unblocks pump's Read if it wasn't already EOF.
	s.ptmx.Close() //nolint:errcheck

	// Wait for pump to finish its last vte.Write before closing the VTE.
	// Required: closing SafeEmulator while pump is mid-Write races on
	// the emulator's internal state (-race flags it).
	<-s.pumpDone

	// Close the VTE response pipe to unblock drainVTE. Do not call vte.Close
	// while drainVTE is inside vte.Read: vt-go's Read and Close race on the
	// emulator's unprotected closed flag. Closing the pipe writer wakes Read
	// without touching that flag.
	pipeClosed := closeVTEPipe(s.vte)

	// Wait for drainVTE to exit fully before proceeding. This ensures no
	// goroutine is accessing the emulator after we return.
	<-s.drainDone

	if pipeClosed {
		s.vte.Close() //nolint:errcheck
	}

	s.mu.Lock()
	s.exitCode = exitCode
	s.exitedAt = time.Now()
	hasClients := len(s.clients) > 0
	s.mu.Unlock()

	if s.manager != nil {
		s.manager.onSessionChanged()
	}

	if !hasClients {
		go s.expireTombstone()
	}
}

type pipeErrorCloser interface {
	CloseWithError(err error) error
}

// closeVTEPipe closes just the VTE's internal pipe writer to unblock
// drainVTE's Read without touching the emulator's closed flag. Returns true
// if only the pipe was closed (caller must still call vte.Close), or false
// if vte.Close was called as a fallback (caller must not double-close).
func closeVTEPipe(vte *vt.SafeEmulator) bool {
	if closer, ok := vte.InputPipe().(pipeErrorCloser); ok {
		closer.CloseWithError(io.EOF) //nolint:errcheck
		return true
	}
	vte.Close() //nolint:errcheck
	return false
}

func (s *Session) expireTombstone() {
	time.Sleep(1 * time.Hour)
	s.cleanupOnce.Do(func() { close(s.cleanup) })
}

func (s *Session) waitForCleanup() {
	<-s.cleanup
}

// ToCRLF normalizes bare \n to \r\n without doubling carriage returns on
// content that already uses \r\n. It is the single source of truth for cooking
// vibed-generated output bound for a raw-mode terminal that does not translate
// \n itself: the reattach replay below, and sshd's selector/stderr writers
// (which wrap it in an io.Writer). Live PTY bytes are deliberately never run
// through it — a raw-mode TUI's bare \n must reach the client intact.
//
// Note: this is a whole-buffer transform. A streaming caller that can split a
// \r\n across two Write calls would double the CR; current callers emit bare
// \n (the collapse step is defensive), so that boundary case never arises.
func ToCRLF(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

// renderVTEScrollback renders the VTE emulator's scrollback buffer (lines
// that have scrolled off the visible screen) as an ANSI byte stream with
// styles preserved. Uses StyleDiff to emit SGR transitions between cells.
// Skips wide-character continuation cells (Width == 0) so CJK content
// aligns correctly. Styled spaces are preserved because they are visible
// (for example, background-colored separators), while default-style right
// edge fill is trimmed. The style is reset at each line boundary so trimmed
// trailing cells can't leak SGR state into the next line. An explicit final
// ResetStyle keeps the subsequent vte.Render() blob starting from a clean
// style state.
func renderVTEScrollback(vte *vt.SafeEmulator) []byte {
	lines := vte.ScrollbackLen()
	if lines == 0 {
		return nil
	}
	width := vte.Width()

	var buf bytes.Buffer
	var prev uv.Style
	for y := range lines {
		// Find the last significant column so we preserve styled spaces but
		// still drop default-style right-edge fill.
		lastCol := -1
		for x := width - 1; x >= 0; x-- {
			c := vte.ScrollbackCellAt(x, y)
			if scrollbackCellSignificant(c) {
				lastCol = x
				break
			}
		}
		for x := 0; x <= lastCol; x++ {
			cell := vte.ScrollbackCellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			if !cell.Style.Equal(&prev) {
				buf.WriteString(uv.StyleDiff(&prev, &cell.Style))
				prev = cell.Style
			}
			if cell.Content == "" {
				buf.WriteByte(' ')
			} else {
				buf.WriteString(cell.Content)
			}
		}
		if !prev.IsZero() {
			buf.WriteString(ansi.ResetStyle)
			prev = uv.Style{}
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(ansi.ResetStyle)
	return buf.Bytes()
}

func scrollbackCellSignificant(cell *uv.Cell) bool {
	if cell == nil || cell.Width == 0 {
		return false
	}
	if cell.Content != "" && cell.Content != " " {
		return true
	}
	return !cell.Style.IsZero()
}

// MergeEnv returns the container's environment with session-provided vars
// overlaid. Filters out vibed-internal config variables
// (VIBEPIT_SSH_PUBKEY).
func MergeEnv(sessionEnv []string) []string {
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

	delete(env, "VIBEPIT_SSH_PUBKEY")

	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
