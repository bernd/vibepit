package session

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Session represents a persistent PTY shell session that survives client
// disconnects. It manages the shell process, PTY, attached clients, and
// output fan-out.
type Session struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	createdAt time.Time
	manager   *Manager

	mu         sync.Mutex
	clients    []*Client
	writer     *Client
	exited     bool
	exitCode   int
	exitedAt   time.Time
	vte        *vt.SafeEmulator
	scrollback *Scrollback
	cols       uint16
	rows       uint16

	vteInput    chan []byte   // async VTE feed channel
	pumpDone    chan struct{} // closed when pump goroutine exits
	cleanup     chan struct{}
	cleanupOnce sync.Once
}

func newSession(id string, cols, rows uint16, env []string, mgr *Manager) (*Session, error) {
	cmd := exec.Command("/bin/bash", "--login")
	cmd.Env = MergeEnv(env)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Session{
		id:         id,
		cmd:        cmd,
		ptmx:       ptmx,
		createdAt:  time.Now(),
		manager:    mgr,
		vte:        vt.NewSafeEmulator(int(cols), int(rows)),
		scrollback: NewScrollback(10000),
		cols:       cols,
		rows:       rows,
		vteInput:   make(chan []byte, 1024),
		pumpDone:   make(chan struct{}),
		cleanup:    make(chan struct{}),
	}

	go s.pump()
	go s.feedVTE()
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
		ID:          s.id,
		Command:     s.cmd.Path,
		ClientCount: len(s.clients),
		CreatedAt:   s.createdAt,
		ExitCode:    s.exitCode,
		ExitedAt:    s.exitedAt,
	}
	if s.exited {
		info.Status = "exited"
	} else if len(s.clients) > 0 {
		info.Status = "attached"
	} else {
		info.Status = "detached"
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

	// Snapshot state while holding the lock.
	altScreen := s.vte.IsAltScreen()
	vteScreen := s.vte.Render()
	cursorPos := s.vte.CursorPosition()
	// Render VTE scrollback (only truly off-screen lines) via the
	// per-cell safe accessor. Our custom Scrollback records all output
	// lines including on-screen ones, so using it would duplicate content.
	scrollbackData := renderVTEScrollback(s.vte)

	c := newClient(s)
	s.clients = append(s.clients, c)

	if s.writer == nil {
		s.writer = c
		pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
	}

	s.mu.Unlock()

	if s.manager != nil {
		go s.manager.onSessionChanged()
	}

	if altScreen {
		// Alternate screen app (vim, less, htop): enter alternate screen
		// on the client side (the PTY is already in alt screen but the
		// new SSH channel isn't), clear it, then send Ctrl-L to the PTY
		// to trigger a full redraw. SIGWINCH only does a partial redraw
		// in some apps; Ctrl-L is the universal "redraw screen" command.
		c.deliver([]byte("\033[?1049h\033[2J"))
		s.ptmx.Write([]byte{0x0c}) //nolint:errcheck // Ctrl-L to PTY
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
			replay = append(replay, fmt.Sprintf("\033[%d;%dH", cursorPos.Y+1, cursorPos.X+1)...)
		}
		if len(replay) > 0 {
			c.deliver(replay)
		}
	}
	// Always SIGWINCH — harmless for shells, triggers redraw for TUI apps.
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
		pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
		s.vte.Resize(int(cols), int(rows))
		s.cols = cols
		s.rows = rows
	}
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
func (s *Session) Resize(c *Client, cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer != c {
		return
	}
	pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
	s.vte.Resize(int(cols), int(rows))
	s.cols = cols
	s.rows = rows
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

// feedVTE runs in its own goroutine and feeds PTY output to the VTE emulator.
// This is async because the VTE must not stall the pump or client delivery.
// It also toggles scrollback pause based on alternate screen state.
func (s *Session) feedVTE() {
	for data := range s.vteInput {
		wasAlt := s.vte.IsAltScreen()
		s.vte.Write(data) //nolint:errcheck
		isAlt := s.vte.IsAltScreen()
		if wasAlt != isAlt {
			s.scrollback.SetPaused(isAlt)
		}
	}
}

// drainVTE continuously reads from the VTE's internal response pipe. The VTE
// generates responses for terminal queries (DA, DSR, cursor position, etc.).
// If this pipe isn't drained, Write() blocks when the buffer fills up.
func (s *Session) drainVTE() {
	buf := make([]byte, 1024)
	for {
		if _, err := s.vte.Read(buf); err != nil {
			return
		}
	}
}

// pump reads PTY output and delivers it to all attached clients.
func (s *Session) pump() {
	defer close(s.pumpDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Update scrollback under lock, snapshot client list, then
			// deliver outside the lock to avoid deadlock with WriteInput.
			s.mu.Lock()
			s.scrollback.Write(data)
			clients := make([]*Client, len(s.clients))
			copy(clients, s.clients)
			s.mu.Unlock()

			// Feed VTE asynchronously. Use select to avoid panic if the
			// channel is closed during session exit.
			select {
			case s.vteInput <- data:
			default:
				slog.Warn("VTE input channel full, dropping data", "session", s.id, "bytes", len(data))
			}

			for _, c := range clients {
				c.deliver(data)
			}
		}
		if err != nil {
			s.mu.Lock()
			clients := make([]*Client, len(s.clients))
			copy(clients, s.clients)
			s.mu.Unlock()

			for _, c := range clients {
				c.closeOutput()
			}
			return
		}
	}
}

// waitForExit waits for the shell process to exit and marks the session as a
// tombstone. If no clients are attached, starts the expiry timer.
func (s *Session) waitForExit() {
	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	s.ptmx.Close() //nolint:errcheck
	<-s.pumpDone   // wait for pump to stop reading before closing vteInput
	close(s.vteInput)
	s.vte.Close() // unblock drainVTE goroutine //nolint:errcheck

	s.mu.Lock()
	s.exited = true
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

func (s *Session) expireTombstone() {
	time.Sleep(1 * time.Hour)
	s.cleanupOnce.Do(func() { close(s.cleanup) })
}

func (s *Session) waitForCleanup() {
	<-s.cleanup
}

// renderVTEScrollback renders the VTE emulator's scrollback buffer (lines
// that have truly scrolled off the visible screen) to a byte slice. Each
// line is plain text with a trailing newline. Uses the per-cell safe
// accessor so it can be called while the VTE is concurrently written to.
func renderVTEScrollback(vte *vt.SafeEmulator) []byte {
	lines := vte.ScrollbackLen()
	if lines == 0 {
		return nil
	}
	width := vte.Width()
	var buf bytes.Buffer
	for y := range lines {
		lineStart := buf.Len()
		for x := range width {
			cell := vte.ScrollbackCellAt(x, y)
			if cell != nil && cell.Content != "" {
				buf.WriteString(cell.Content)
			} else {
				buf.WriteByte(' ')
			}
		}
		// Trim trailing spaces from each line.
		line := buf.Bytes()[lineStart:]
		trimmed := bytes.TrimRight(line, " ")
		buf.Truncate(lineStart + len(trimmed))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// MergeEnv returns the container's environment with session-provided vars
// overlaid. Filters out vibed-internal config variables
// (VIBEPIT_SSH_PUBKEY, VIBEPIT_DEFAULT_COMMAND).
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
	delete(env, "VIBEPIT_DEFAULT_COMMAND")

	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
