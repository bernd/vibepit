# Persistent PTY Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make shell sessions survive SSH disconnects by managing persistent PTY processes in `vibed`, with VTE-based screen restore and a Bubble Tea session selector.

**Architecture:** A new `session/` package owns PTY processes, a virtual terminal emulator, and a scrollback buffer. The SSH handler in `sshd/` attaches/detaches SSH channels to sessions. A Bubble Tea selector lets users pick which session to join. The session package knows nothing about SSH — it exposes `io.ReadWriteCloser` clients.

**Tech Stack:** Go, `creack/pty`, `charmbracelet/x/vt`, `charmbracelet/bubbletea`, `charmbracelet/ssh`

**Spec:** `docs/superpowers/specs/2026-04-08-persistent-pty-sessions-design.md`

---

### Task 1: Scrollback ring buffer

**Files:**
- Create: `session/scrollback.go`
- Create: `session/scrollback_test.go`

A byte-oriented ring buffer that stores the last N lines of terminal output.
Lines are delimited by `\n`. The buffer stores raw bytes including ANSI escape
sequences.

- [ ] **Step 1: Write failing test for scrollback buffer**

```go
package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScrollback_WriteThenSnapshot(t *testing.T) {
	sb := NewScrollback(5)
	sb.Write([]byte("line1\nline2\nline3\n"))
	snap := sb.Snapshot()
	assert.Equal(t, "line1\nline2\nline3\n", string(snap))
}

func TestScrollback_Overflow(t *testing.T) {
	sb := NewScrollback(3)
	sb.Write([]byte("a\nb\nc\nd\ne\n"))
	snap := sb.Snapshot()
	assert.Equal(t, "c\nd\ne\n", string(snap))
}

func TestScrollback_PartialLine(t *testing.T) {
	sb := NewScrollback(5)
	sb.Write([]byte("partial"))
	sb.Write([]byte(" line\ncomplete\n"))
	snap := sb.Snapshot()
	assert.Equal(t, "partial line\ncomplete\n", string(snap))
}

func TestScrollback_Empty(t *testing.T) {
	sb := NewScrollback(5)
	snap := sb.Snapshot()
	assert.Empty(t, snap)
}

func TestScrollback_Pause(t *testing.T) {
	sb := NewScrollback(5)
	sb.Write([]byte("before\n"))
	sb.SetPaused(true)
	sb.Write([]byte("during\n"))
	sb.SetPaused(false)
	sb.Write([]byte("after\n"))
	snap := sb.Snapshot()
	assert.Equal(t, "before\nafter\n", string(snap))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./session/ -run TestScrollback -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement scrollback buffer**

```go
package session

import "sync"

// Scrollback is a line-oriented ring buffer that stores the last N lines of
// terminal output. It is safe for concurrent use.
type Scrollback struct {
	mu      sync.Mutex
	lines   [][]byte
	maxLines int
	partial []byte // incomplete line (no trailing \n yet)
	paused  bool
}

// NewScrollback creates a scrollback buffer that retains up to maxLines lines.
func NewScrollback(maxLines int) *Scrollback {
	return &Scrollback{
		lines:    make([][]byte, 0, maxLines),
		maxLines: maxLines,
	}
}

// Write processes raw terminal output, splitting it into lines and appending
// them to the ring buffer. Partial lines (no trailing \n) are buffered until
// the next Write completes them.
func (s *Scrollback) Write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused {
		return
	}

	for len(p) > 0 {
		idx := -1
		for i, b := range p {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx == -1 {
			// No newline — accumulate as partial.
			s.partial = append(s.partial, p...)
			return
		}
		// Complete line: partial + up to and including \n.
		line := make([]byte, 0, len(s.partial)+idx+1)
		line = append(line, s.partial...)
		line = append(line, p[:idx+1]...)
		s.partial = s.partial[:0]
		p = p[idx+1:]

		s.appendLine(line)
	}
}

func (s *Scrollback) appendLine(line []byte) {
	if len(s.lines) >= s.maxLines {
		// Shift left.
		copy(s.lines, s.lines[1:])
		s.lines[len(s.lines)-1] = line
	} else {
		s.lines = append(s.lines, line)
	}
}

// Snapshot returns a copy of all buffered lines concatenated.
func (s *Scrollback) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	size := 0
	for _, l := range s.lines {
		size += len(l)
	}
	buf := make([]byte, 0, size)
	for _, l := range s.lines {
		buf = append(buf, l...)
	}
	return buf
}

// SetPaused controls whether writes are captured. When paused (e.g., during
// alternate screen mode), output is discarded.
func (s *Scrollback) SetPaused(paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = paused
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./session/ -run TestScrollback -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add session/
git commit -m "feat: add scrollback ring buffer for persistent sessions"
```

---

### Task 2: Session Manager and Session core

**Files:**
- Create: `session/manager.go`
- Create: `session/session.go`
- Create: `session/client.go`
- Create: `session/manager_test.go`

This task creates the core types with basic lifecycle (create, attach, detach,
shell exit) but WITHOUT VTE integration. Output is fanned out directly to
clients. VTE and scrollback are wired in Task 3.

- [ ] **Step 1: Write failing test for Manager create and list**

```go
package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_CreateAndList(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24)
	require.NoError(t, err)
	require.NotNil(t, s)

	sessions := m.List()
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
	assert.Equal(t, "/bin/bash", sessions[0].Command)
	assert.Equal(t, 0, sessions[0].ClientCount)
}

func TestManager_Limit(t *testing.T) {
	m := NewManager(1)
	_, err := m.Create(80, 24)
	require.NoError(t, err)
	_, err = m.Create(80, 24)
	require.Error(t, err)
}

func TestManager_Get(t *testing.T) {
	m := NewManager(50)
	s, _ := m.Create(80, 24)
	got := m.Get(s.ID())
	assert.Equal(t, s, got)
	assert.Nil(t, m.Get("nonexistent"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./session/ -run TestManager -v`
Expected: FAIL — types don't exist

- [ ] **Step 3: Implement SessionInfo and Manager**

Create `session/manager.go`:

```go
package session

import (
	"fmt"
	"sync"
	"time"
)

// SessionInfo is a snapshot of session metadata for display.
type SessionInfo struct {
	ID          string
	Command     string
	ClientCount int
	Status      string // "attached", "detached", "exited"
	ExitCode    int
	CreatedAt   time.Time
	ExitedAt    time.Time
}

// Manager owns all persistent sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	counter  int
	limit    int
}

// NewManager creates a session manager with the given maximum session count.
func NewManager(limit int) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		limit:    limit,
	}
}

// Create spawns a new shell session with the given terminal size.
func (m *Manager) Create(cols, rows uint16) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	for _, s := range m.sessions {
		if !s.Exited() {
			active++
		}
	}
	if active >= m.limit {
		return nil, fmt.Errorf("session limit reached (%d)", m.limit)
	}

	m.counter++
	id := fmt.Sprintf("session-%d", m.counter)
	s, err := newSession(id, cols, rows)
	if err != nil {
		return nil, err
	}
	m.sessions[id] = s

	// Remove session from manager when it becomes a tombstone and expires.
	go func() {
		s.waitForCleanup()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()

	return s, nil
}

// Get returns the session with the given ID, or nil.
func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// List returns info for all sessions (running and tombstoned).
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	return infos
}
```

- [ ] **Step 4: Implement Session**

Create `session/session.go`:

```go
package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Session is a persistent PTY shell process with attached clients.
type Session struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	createdAt time.Time

	mu       sync.Mutex
	clients  []*Client
	writer   *Client // the client that can send input and resize
	exited   bool
	exitCode int
	exitedAt time.Time

	// cleanup is closed when the tombstone should be removed from the manager.
	cleanup chan struct{}
}

func newSession(id string, cols, rows uint16) (*Session, error) {
	cmd := exec.Command("/bin/bash", "--login")
	cmd.Env = mergeEnv(nil)

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
		createdAt: time.Now(),
		cleanup:   make(chan struct{}),
	}

	go s.pump()
	go s.waitForExit()

	return s, nil
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.id }

// Exited reports whether the shell process has exited.
func (s *Session) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// Info returns a snapshot of session metadata.
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

// Attach creates a new client attached to this session.
func (s *Session) Attach(cols, rows uint16) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()

	c := newClient(s)
	s.clients = append(s.clients, c)

	// First client (or first after all disconnected) becomes writer.
	if s.writer == nil {
		s.writer = c
		pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
	}

	return c
}

// TakeOver promotes the given client to writer, demoting the current writer.
func (s *Session) TakeOver(c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer = c
}

// Detach removes a client from the session.
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
}

// Resize changes the PTY size. Only effective if called by the writer.
func (s *Session) Resize(c *Client, cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer != c {
		return
	}
	pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
}

// WriteInput sends input to the PTY. Only effective if called by the writer.
func (s *Session) WriteInput(c *Client, p []byte) (int, error) {
	s.mu.Lock()
	if s.writer != c {
		s.mu.Unlock()
		return 0, fmt.Errorf("not the writer")
	}
	s.mu.Unlock()
	return s.ptmx.Write(p)
}

// pump reads PTY output and fans it out to all attached clients.
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.Lock()
			for _, c := range s.clients {
				c.deliver(data)
			}
			s.mu.Unlock()
		}
		if err != nil {
			// PTY closed — shell exited.
			s.mu.Lock()
			for _, c := range s.clients {
				c.closeOutput()
			}
			s.mu.Unlock()
			return
		}
	}
}

// waitForExit waits for the shell process to exit and marks the session as a
// tombstone. If no clients are attached, starts the tombstone expiry timer.
func (s *Session) waitForExit() {
	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	s.ptmx.Close() //nolint:errcheck

	s.mu.Lock()
	s.exited = true
	s.exitCode = exitCode
	s.exitedAt = time.Now()
	hasClients := len(s.clients) > 0
	s.mu.Unlock()

	if !hasClients {
		// Start tombstone expiry.
		go s.expireTombstone()
	}
}

func (s *Session) expireTombstone() {
	time.Sleep(1 * time.Hour)
	close(s.cleanup)
}

func (s *Session) waitForCleanup() {
	<-s.cleanup
}
```

Note: `mergeEnv` is currently in `sshd/server.go`. For now, add a package-level
`mergeEnv` function to `session/session.go` that does the same thing (merge
`os.Environ()` with provided env, filter vibed config vars). We'll refactor the
duplication when wiring SSH in Task 5.

- [ ] **Step 5: Implement Client**

Create `session/client.go`:

```go
package session

import (
	"io"
	"sync"
)

// Client is a reader/writer attached to a session. Output is delivered via an
// internal channel. Input is forwarded to the session's PTY via WriteInput.
type Client struct {
	session   *Session
	output    chan []byte
	done      chan struct{}
	closeOnce sync.Once
	outOnce   sync.Once
}

func newClient(s *Session) *Client {
	return &Client{
		session: s,
		output:  make(chan []byte, 256),
		done:    make(chan struct{}),
	}
}

// Read returns output from the session. Blocks until data is available or the
// session ends.
func (c *Client) Read(p []byte) (int, error) {
	select {
	case data, ok := <-c.output:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, data)
		// If p is too small, remaining data is lost. In practice, callers
		// use large buffers (32KB+).
		return n, nil
	case <-c.done:
		return 0, io.EOF
	}
}

// Write sends input to the session's PTY. Only the writer client can send
// input; read-only clients get an error.
func (c *Client) Write(p []byte) (int, error) {
	return c.session.WriteInput(c, p)
}

// Close detaches this client from the session.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.session.Detach(c)
	})
	return nil
}

// deliver queues output data for this client. Non-blocking — drops data if
// the client's buffer is full (slow consumer protection).
func (c *Client) deliver(data []byte) {
	select {
	case c.output <- data:
	default:
		// Slow consumer — drop data to avoid stalling the pump.
	}
}

// closeOutput signals that no more output will be delivered.
func (c *Client) closeOutput() {
	c.outOnce.Do(func() {
		close(c.output)
	})
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./session/ -v`
Expected: PASS

- [ ] **Step 7: Run full test suite**

Run: `make test`
Expected: All pass

- [ ] **Step 8: Commit**

```bash
git add session/
git commit -m "feat: add session manager with PTY, client attach/detach, and fan-out"
```

---

### Task 3: VTE integration and replay

**Files:**
- Modify: `session/session.go`
- Create: `session/session_test.go`

Wire the VTE (`charmbracelet/x/vt`) and scrollback buffer into the session's
pump. Implement the replay-on-attach flow.

- [ ] **Step 1: Add VTE dependency**

```bash
go get github.com/charmbracelet/x/vt
go mod tidy
```

- [ ] **Step 2: Write failing test for replay**

```go
package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSession_ReplayOnAttach(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24)
	require.NoError(t, err)

	// Attach first client (writer) and send a command.
	c1 := s.Attach(80, 24)
	_, err = c1.Write([]byte("echo hello\n"))
	require.NoError(t, err)

	// Wait for output to flow.
	time.Sleep(200 * time.Millisecond)

	// Detach first client (simulates disconnect).
	c1.Close()

	// Attach second client — should receive replay.
	c2 := s.Attach(80, 24)
	buf := make([]byte, 4096)
	n, err := c2.Read(buf)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "should receive replay output")
	c2.Close()
}
```

- [ ] **Step 3: Run test to verify it fails or produces empty replay**

Run: `go test ./session/ -run TestSession_ReplayOnAttach -v -timeout 10s`

- [ ] **Step 4: Add VTE and scrollback to Session**

In `session/session.go`, add fields to the `Session` struct:

```go
import "github.com/charmbracelet/x/vt"
```

Add to Session struct:
```go
	vte       *vt.SafeEmulator
	scrollback *Scrollback
	cols      uint16
	rows      uint16
```

In `newSession`, initialize them:
```go
	s.cols = cols
	s.rows = rows
	s.vte = vt.NewSafeEmulator(int(cols), int(rows))
	s.scrollback = NewScrollback(10000)
```

Update `pump()` to feed VTE and scrollback:
```go
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.Lock()
			// Feed VTE.
			s.vte.Write(data) //nolint:errcheck
			// Feed scrollback (paused during alternate screen).
			s.scrollback.Write(data)
			// Fan out to clients.
			for _, c := range s.clients {
				c.deliver(data)
			}
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			for _, c := range s.clients {
				c.closeOutput()
			}
			s.mu.Unlock()
			return
		}
	}
}
```

Update `Attach` to replay VTE state (snapshot-plus-queue):
```go
func (s *Session) Attach(cols, rows uint16) *Client {
	s.mu.Lock()

	c := newClient(s)
	s.clients = append(s.clients, c)

	// Snapshot under lock, register for live delivery.
	scrollSnap := s.scrollback.Snapshot()
	vteSnap := s.vte.String() // renders current screen state

	if s.writer == nil {
		s.writer = c
		s.cols = cols
		s.rows = rows
		pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}) //nolint:errcheck
		s.vte.Resize(int(cols), int(rows))
	}

	s.mu.Unlock()

	// Send replay without holding the lock.
	go func() {
		// Terminal reset + scrollback + VTE screen.
		c.deliver([]byte("\033c")) // reset
		if len(scrollSnap) > 0 {
			c.deliver(scrollSnap)
		}
		if len(vteSnap) > 0 {
			c.deliver([]byte(vteSnap))
		}
	}()

	return c
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./session/ -v -timeout 30s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add session/ go.mod go.sum
git commit -m "feat: add VTE and scrollback replay to persistent sessions"
```

---

### Task 4: Session state file

**Files:**
- Modify: `session/manager.go`
- Create: `session/statefile.go`
- Create: `session/statefile_test.go`

The manager writes a JSON state file on every state change.

- [ ] **Step 1: Write failing test**

```go
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateFile_WrittenOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	m := NewManager(50)
	m.SetStateFilePath(path)

	_, err := m.Create(80, 24)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var sessions []SessionInfo
	require.NoError(t, json.Unmarshal(data, &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./session/ -run TestStateFile -v`

- [ ] **Step 3: Implement state file writing**

Create `session/statefile.go`:

```go
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// writeStateFile atomically writes the session list to the state file path.
func (m *Manager) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	data, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		return
	}
	tmp := m.stateFilePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, m.stateFilePath) //nolint:errcheck
}
```

Add `stateFilePath` field and setter to `Manager`:

```go
// In manager.go, add to Manager struct:
	stateFilePath string

// SetStateFilePath sets the path for the JSON session state file.
func (m *Manager) SetStateFilePath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateFilePath = path
	os.MkdirAll(filepath.Dir(path), 0755) //nolint:errcheck
}
```

Call `m.writeStateFile()` at the end of `Create` (while still holding the lock).

- [ ] **Step 4: Wire state file updates into session events**

The manager needs to know when sessions change state (attach, detach, exit).
Add a callback mechanism: the session calls `m.onSessionChanged()` which
writes the state file under the manager's lock.

Add to `Manager`:
```go
func (m *Manager) onSessionChanged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeStateFile()
}
```

Pass the manager reference to sessions so they can call this callback on
attach, detach, and exit. Update `Session.Attach`, `Session.Detach`, and
`Session.waitForExit` to call `s.manager.onSessionChanged()` after state
changes.

- [ ] **Step 5: Run tests**

Run: `go test ./session/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add session/
git commit -m "feat: add session state file for external observability"
```

---

### Task 5: Wire sessions into SSH handler

**Files:**
- Modify: `sshd/server.go`
- Modify: `cmd/vibed.go`
- Create: `sshd/server_test.go` (update existing)

Replace the current `handlePTYSession` with session-based attach/detach.
Keep `handleExecSession` unchanged.

- [ ] **Step 1: Update `NewServer` to accept a session manager**

Change the `Config` struct and `NewServer` in `sshd/server.go`:

```go
type Config struct {
	HostKeyPEM    []byte
	AuthorizedKey []byte
	Sessions      *session.Manager // persistent session manager
}
```

Store the manager and pass it to the handler via a closure or server field.
The `handleSession` function needs access to it.

- [ ] **Step 2: Replace `handlePTYSession` with session-based logic**

The new PTY handler:
1. If zero sessions exist, create one and attach.
2. If one or more sessions exist, show the selector (Task 6 — for now, just
   auto-attach to the first detached session or create a new one).
3. Wire SSH channel I/O to the session client.
4. Forward SIGWINCH from the SSH channel to `session.Resize`.
5. On SSH channel close, call `client.Close()` (which triggers detach).

```go
func handlePTYSession(mgr *session.Manager, sess charmssh.Session, ptyReq charmssh.Pty, winCh <-chan charmssh.Window) {
	cols := uint16(ptyReq.Window.Width)
	rows := uint16(ptyReq.Window.Height)

	// Find or create a session (simplified — selector comes in Task 6).
	sessions := mgr.List()
	var s *session.Session
	for _, info := range sessions {
		if info.Status == "detached" {
			s = mgr.Get(info.ID)
			break
		}
	}
	if s == nil {
		var err error
		s, err = mgr.Create(cols, rows)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
			sess.Exit(1) //nolint:errcheck
			return
		}
	}

	client := s.Attach(cols, rows)
	defer client.Close()

	// Forward window resize.
	go func() {
		for win := range winCh {
			s.Resize(client, uint16(win.Width), uint16(win.Height))
		}
	}()

	// Copy SSH stdin to session (writer only).
	go func() {
		io.Copy(client, sess) //nolint:errcheck
	}()

	// Copy session output to SSH channel.
	done := make(chan struct{})
	go func() {
		io.Copy(sess, client) //nolint:errcheck
		close(done)
	}()

	<-done
	sess.Exit(0) //nolint:errcheck
}
```

- [ ] **Step 3: Move `mergeEnv` to `session/` package**

Move `mergeEnv` from `sshd/server.go` to `session/env.go` (or keep it in
`session/session.go`). Export it as `MergeEnv`. Update `handleExecSession` to
call `session.MergeEnv`.

- [ ] **Step 4: Update `cmd/vibed.go` to create and pass the manager**

```go
func VibedAction(ctx context.Context, cmd *cli.Command) error {
	// ... existing init code ...

	mgr := session.NewManager(50)
	mgr.SetStateFilePath("/tmp/vibed-sessions.json")

	srv, err := sshd.NewServer(sshd.Config{
		HostKeyPEM:    hostKey,
		AuthorizedKey: []byte(authorizedKey),
		Sessions:      mgr,
	})
	// ... rest unchanged ...
}
```

- [ ] **Step 5: Configure SSH keepalives**

In `NewServer`, set keepalive options on the charmssh server:

```go
srv.ServerConfigCallback = func(ctx charmssh.Context) *gossh.ServerConfig {
	cfg := &gossh.ServerConfig{}
	// Keepalive is handled at the SSH library level — set
	// IdleTimeout and MaxTimeout on the charmssh.Server.
	return cfg
}
srv.IdleTimeout = 0 // no idle timeout on sessions
```

Check what `charmssh.Server` exposes for keepalive settings and configure
interval=2s, max=2.

- [ ] **Step 6: Update existing tests**

Update `sshd/server_test.go` to pass a `Sessions` manager in the config:

```go
mgr := session.NewManager(50)
srv, err := NewServer(Config{
	HostKeyPEM:    hostPriv,
	AuthorizedKey: clientPub,
	Sessions:      mgr,
})
```

- [ ] **Step 7: Run tests**

Run: `go test ./sshd/ -v -timeout 30s && go test ./session/ -v && make test`
Expected: All pass

- [ ] **Step 8: Commit**

```bash
git add sshd/ session/ cmd/vibed.go
git commit -m "feat: wire persistent sessions into SSH handler"
```

---

### Task 6: Bubble Tea session selector

**Files:**
- Create: `sshd/selector.go`
- Modify: `sshd/server.go`

Replace the simplified session selection from Task 5 with a Bubble Tea TUI
selector shown over the SSH connection.

- [ ] **Step 1: Create the selector model**

Create `sshd/selector.go`:

```go
package sshd

import (
	"fmt"
	"time"

	"github.com/bernd/vibepit/session"
	tea "github.com/charmbracelet/bubbletea"
)

type selectorResult struct {
	sessionID string // empty means "new session"
	takeOver  bool   // true if user wants to take over an attached session
}

type selectorModel struct {
	sessions []session.SessionInfo
	cursor   int
	result   *selectorResult
	confirm  bool // showing takeover confirmation
}

func newSelectorModel(sessions []session.SessionInfo) selectorModel {
	return selectorModel{
		sessions: sessions,
	}
}

func (m selectorModel) Init() tea.Cmd { return nil }

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions) { // +1 for "new session"
				m.cursor++
			}
		case "n":
			m.result = &selectorResult{}
			return m, tea.Quit
		case "enter":
			if m.confirm {
				m.result = &selectorResult{
					sessionID: m.sessions[m.cursor].ID,
					takeOver:  true,
				}
				return m, tea.Quit
			}
			if m.cursor == len(m.sessions) {
				// "New session" selected.
				m.result = &selectorResult{}
				return m, tea.Quit
			}
			info := m.sessions[m.cursor]
			if info.Status == "exited" {
				// Can't attach to tombstone — ignore.
				return m, nil
			}
			if info.Status == "attached" {
				// Prompt for takeover.
				m.confirm = true
				return m, nil
			}
			m.result = &selectorResult{sessionID: info.ID}
			return m, tea.Quit
		case "y":
			if m.confirm {
				m.result = &selectorResult{
					sessionID: m.sessions[m.cursor].ID,
					takeOver:  true,
				}
				return m, tea.Quit
			}
		case "esc":
			if m.confirm {
				m.confirm = false
				return m, nil
			}
			// Attach read-only if user presses escape on attached session.
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectorModel) View() string {
	if m.confirm {
		info := m.sessions[m.cursor]
		return fmt.Sprintf("\nSession %s has %d client(s) attached.\nTake over as writer? [y/n] ", info.ID, info.ClientCount)
	}

	s := "\nSessions:\n"
	for i, info := range m.sessions {
		cursor := "  "
		if m.cursor == i {
			cursor = "> "
		}
		status := formatStatus(info)
		s += fmt.Sprintf("%s[%d] %s (%s) — %s\n", cursor, i+1, info.ID, info.Command, status)
	}
	// "New session" option.
	cursor := "  "
	if m.cursor == len(m.sessions) {
		cursor = "> "
	}
	s += fmt.Sprintf("%s[n] new session\n", cursor)
	s += "\nUse ↑/↓ to navigate, Enter to select, n for new session, q to quit\n"
	return s
}

func formatStatus(info session.SessionInfo) string {
	switch info.Status {
	case "attached":
		return fmt.Sprintf("%d client(s) attached", info.ClientCount)
	case "detached":
		return fmt.Sprintf("detached %s ago", time.Since(info.CreatedAt).Truncate(time.Second))
	case "exited":
		return fmt.Sprintf("exited (%d) %s ago", info.ExitCode, time.Since(info.ExitedAt).Truncate(time.Second))
	default:
		return info.Status
	}
}
```

- [ ] **Step 2: Wire selector into `handlePTYSession`**

Update `handlePTYSession` in `sshd/server.go` to use the selector when
sessions exist:

```go
func handlePTYSession(mgr *session.Manager, sess charmssh.Session, ptyReq charmssh.Pty, winCh <-chan charmssh.Window) {
	cols := uint16(ptyReq.Window.Width)
	rows := uint16(ptyReq.Window.Height)

	sessions := mgr.List()

	var s *session.Session
	var takeOver bool

	if len(sessions) == 0 {
		// No sessions — create one.
		var err error
		s, err = mgr.Create(cols, rows)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "create session: %s\n", err)
			sess.Exit(1)
			return
		}
	} else {
		// Show selector.
		model := newSelectorModel(sessions)
		p := tea.NewProgram(model, tea.WithInput(sess), tea.WithOutput(sess))
		finalModel, err := p.Run()
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "selector: %s\n", err)
			sess.Exit(1)
			return
		}
		result := finalModel.(selectorModel).result
		if result == nil {
			// User quit without selecting.
			sess.Exit(0)
			return
		}
		if result.sessionID == "" {
			// New session.
			s, err = mgr.Create(cols, rows)
			if err != nil {
				fmt.Fprintf(sess.Stderr(), "create session: %s\n", err)
				sess.Exit(1)
				return
			}
		} else {
			s = mgr.Get(result.sessionID)
			if s == nil {
				fmt.Fprintf(sess.Stderr(), "session %s not found\n", result.sessionID)
				sess.Exit(1)
				return
			}
			takeOver = result.takeOver
		}
	}

	client := s.Attach(cols, rows)
	defer client.Close()

	if takeOver {
		s.TakeOver(client)
	}

	// ... rest of I/O wiring same as Task 5 ...
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./sshd/ -v -timeout 30s && make test`
Expected: All pass

- [ ] **Step 4: Commit**

```bash
git add sshd/
git commit -m "feat: add Bubble Tea session selector for persistent sessions"
```

---

### Task 7: Integration and cleanup

**Files:**
- Modify: `cmd/status.go` (show session count)
- Run: integration tests

- [ ] **Step 1: Update `vibepit status` to show session count**

In `cmd/status.go`, after displaying container info, read the session state
file via SSH command mode and display session count:

```go
// After existing status output...
sessionData, err := runSSHCommand(ctx, client, session, "cat /tmp/vibed-sessions.json")
if err == nil {
	var sessions []struct {
		ID     string `json:"ID"`
		Status string `json:"Status"`
	}
	if json.Unmarshal(sessionData, &sessions) == nil {
		active := 0
		for _, s := range sessions {
			if s.Status != "exited" {
				active++
			}
		}
		tui.Status("Sessions", "%d active", active)
	}
}
```

Note: `runSSHCommand` would need to be extracted from the SSH client logic or
implemented as a helper. This is a stretch goal — the core feature works
without it. If time is short, just add a TODO comment and skip for now.

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: All pass

- [ ] **Step 3: Manual integration testing**

Test from the host (requires Docker):

```bash
# Start sandbox.
go run . up

# Connect — should create session-1.
go run . ssh
# Type some commands, then Ctrl-C the ssh client (simulates disconnect).

# Reconnect — should show selector with session-1 (detached).
go run . ssh
# Select session-1 — should see previous output replayed.

# Test command mode (fire-and-forget, no session).
go run . ssh -- echo hello

# Test multiple sessions.
go run . ssh  # select "new session" → session-2
# Open another terminal:
go run . ssh  # should show selector with session-1 and session-2

# Teardown.
go run . down
```

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "feat: persistent PTY sessions integration and status"
```
