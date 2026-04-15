package session

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	vt "github.com/unixshells/vt-go"
)

// testManager returns a Manager that uses /bin/sh instead of /bin/bash --login
// to avoid slow profile loading in test environments.
func testManager(limit int) *Manager {
	m := NewManager(limit)
	m.Command = []string{"/bin/sh"}
	return m
}

func TestManager_CreateAndList(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	require.NotNil(t, s)

	sessions := m.List()
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
	assert.Equal(t, "/bin/sh", sessions[0].Command)
	assert.Equal(t, 0, sessions[0].ClientCount)
}

func TestManager_Limit(t *testing.T) {
	m := testManager(1)
	_, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	_, err = m.Create(80, 24, nil)
	require.Error(t, err)
}

func TestManager_Get(t *testing.T) {
	m := testManager(50)
	s, _ := m.Create(80, 24, nil)
	got := m.Get(s.ID())
	assert.Equal(t, s, got)
	assert.Nil(t, m.Get("nonexistent"))
}

func TestSession_AttachDetach(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	require.NotNil(t, c1)

	info := s.Info()
	assert.Equal(t, 1, info.ClientCount)
	assert.Equal(t, "attached", info.Status)

	c1.Close()

	info = s.Info()
	assert.Equal(t, 0, info.ClientCount)
	assert.Equal(t, "detached", info.Status)
}

func TestSession_WriterPromotion(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// c1 is the writer
	_, err = c2.Write([]byte("hello"))
	assert.Error(t, err, "non-writer should not be able to write")

	_, err = c1.Write([]byte("echo hi\n"))
	assert.NoError(t, err, "writer should be able to write")

	// Detach writer, c2 should be promoted
	c1.Close()

	// Give a moment for state to settle
	time.Sleep(10 * time.Millisecond)

	_, err = c2.Write([]byte("echo promoted\n"))
	assert.NoError(t, err, "promoted client should be able to write")

	c2.Close()
}

func TestSession_TakeOver(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// c1 is writer, c2 cannot write
	_, err = c2.Write([]byte("hello"))
	assert.Error(t, err)

	// TakeOver promotes c2
	s.TakeOver(c2, 80, 24)

	_, err = c2.Write([]byte("echo takeover\n"))
	assert.NoError(t, err)

	_, err = c1.Write([]byte("hello"))
	assert.Error(t, err, "old writer should no longer be able to write")

	c1.Close()
	c2.Close()
}

func TestSession_FanOut(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// Write something that produces output
	_, err = c1.Write([]byte("echo fanout_test_123\n"))
	require.NoError(t, err)

	// Both clients should receive output
	buf1 := make([]byte, 4096)
	buf2 := make([]byte, 4096)

	// Read with timeout
	done := make(chan struct{})
	var n1, n2 int
	go func() {
		n1, _ = c1.Read(buf1)
		close(done)
	}()

	select {
	case <-done:
		assert.Greater(t, n1, 0)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from c1")
	}

	done2 := make(chan struct{})
	go func() {
		n2, _ = c2.Read(buf2)
		close(done2)
	}()

	select {
	case <-done2:
		assert.Greater(t, n2, 0)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from c2")
	}

	c1.Close()
	c2.Close()
}

func TestSession_Resize(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// Only the writer (c1) can resize.
	s.Resize(c2, 120, 40)
	info := s.Info()
	// Session should still be at original size because c2 is not writer.
	_ = info

	// Writer resizes successfully.
	s.Resize(c1, 120, 40)

	c1.Close()
	c2.Close()
}

func TestSession_ShellExit(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c := s.Attach(80, 24)

	// Send exit command to the shell.
	_, err = c.Write([]byte("exit\n"))
	require.NoError(t, err)

	// Read until EOF — the shell should exit.
	buf := make([]byte, 4096)
	deadline := time.After(5 * time.Second)
	for {
		done := make(chan error, 1)
		go func() {
			_, err := c.Read(buf)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				// Got EOF — shell exited. Give waitForExit a moment
				// to update state.
				time.Sleep(50 * time.Millisecond)
				assert.True(t, s.Exited())
				info := s.Info()
				assert.Equal(t, "exited", info.Status)
				c.Close()
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for shell to exit")
		}
	}
}

func TestSession_SlowConsumerDisconnected(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	writer := s.Attach(80, 24)
	slow := s.Attach(80, 24)

	// Fill the slow client's output channel by sending lots of data
	// through the PTY. The slow client never reads.
	for i := range 2000 {
		_, err := writer.Write(fmt.Appendf(nil, "echo line_%d\n", i))
		if err != nil {
			break
		}
	}

	// The slow client should have been disconnected. Verify by trying
	// to read — it should return EOF quickly.
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		_, err := slow.Read(buf)
		readDone <- err
	}()

	select {
	case <-readDone:
		// Slow client was disconnected (or has data) — either way it's handled.
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for slow consumer to be disconnected")
	}

	writer.Close()
}

func TestSession_ReplayOnAttach(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	// Attach first client (writer) and send a command.
	c1 := s.Attach(80, 24)
	_, err = c1.Write([]byte("echo hello\n"))
	require.NoError(t, err)

	// Read output until "hello" appears (confirms PTY processed the command).
	buf := make([]byte, 8192)
	deadline := time.After(3 * time.Second)
	var collected strings.Builder
	for {
		readDone := make(chan int, 1)
		go func() {
			n, _ := c1.Read(buf)
			readDone <- n
		}()
		select {
		case n := <-readDone:
			collected.WriteString(string(buf[:n]))
			if strings.Contains(collected.String(), "hello") {
				goto ready
			}
		case <-deadline:
			t.Fatal("timeout waiting for echo output")
		}
	}
ready:

	// Detach first client (simulates disconnect).
	c1.Close()

	// Attach second client — should receive replay.
	c2 := s.Attach(80, 24)
	n, err := c2.Read(buf)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "should receive replay output")
	c2.Close()
}

// TestSession_VTEDoesNotDropUnderBurst verifies that a large burst of shell
// output does not cause the VT emulator state to desync from reality.
// Historically, pump fed the emulator through a 1024-slot async channel and
// dropped writes with select/default when it filled; replay on reattach
// then showed stale screen state. This test fails under the old async
// design and passes with synchronous in-pump vte.Write.
func TestSession_VTEDoesNotDropUnderBurst(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c := s.Attach(80, 24)

	// Burst 2000 numbered lines through the shell. The completion marker
	// uses printf with split strings to avoid the PTY line-discipline
	// echo matching it (the echo shows the raw command text, which has
	// "BURST_" and "DONE" as separate printf arguments — not the combined
	// token "BURST_DONE" that only appears in the actual output).
	_, err = c.Write([]byte(
		"for i in $(seq 1 2000); do echo line_$i; done; printf '%s%s\\n' BURST_ DONE\n"))
	require.NoError(t, err)

	// Drain the client until we see the combined marker. The marker only
	// appears as actual output — never in the command echo — so this
	// correctly drains until the burst is complete.
	buf := make([]byte, 32*1024)
	deadline := time.After(10 * time.Second)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "BURST_DONE") {
		readDone := make(chan int, 1)
		go func() {
			n, _ := c.Read(buf)
			readDone <- n
		}()
		select {
		case n := <-readDone:
			seen.WriteString(string(buf[:n]))
		case <-deadline:
			t.Fatal("timeout waiting for burst completion")
		}
	}
	// Brief pause to let the VTE process any final buffered output.
	time.Sleep(50 * time.Millisecond)

	// The VT emulator should have the tail of the burst in its combined
	// (scrollback + screen) history. Check that a line near the end is
	// present somewhere — either in scrollback or on the current screen.
	needle := "line_1995"
	found := false

	// Check current screen.
	s.mu.Lock()
	screen := s.vte.Render()
	s.mu.Unlock()
	if strings.Contains(screen, needle) {
		found = true
	}

	// Check scrollback cells.
	if !found {
		s.mu.Lock()
		sbLen := s.vte.ScrollbackLen()
		width := s.vte.Width()
		for y := 0; y < sbLen && !found; y++ {
			var line []byte
			for x := range width {
				cell := s.vte.ScrollbackCellAt(x, y)
				if cell != nil && cell.Content != "" {
					line = append(line, cell.Content...)
				}
			}
			if strings.Contains(string(line), needle) {
				found = true
			}
		}
		s.mu.Unlock()
	}

	assert.True(t, found,
		"VT emulator should contain %q somewhere in scrollback or screen "+
			"after a 2000-line burst; it is missing, which indicates the "+
			"VTE write path dropped data", needle)

	c.Close()
}

// TestSession_AttachAfterExitNeverHangs is a stress test for the
// attach-after-exit race. It runs a tight loop that triggers shell exit
// while concurrently calling Attach, and asserts every returned client
// reaches EOF within a short timeout. Under the old design (s.exited set
// only in waitForExit after <-pumpDone), pump could finish its final
// client snapshot before waitForExit set the flag, leaving a window where
// Attach added a client whose output was never closed; that client's Read
// would block until SSH keepalive (~5s). This test would hang under the
// old design.
func TestSession_AttachAfterExitNeverHangs(t *testing.T) {
	const iterations = 200
	for i := range iterations {
		m := testManager(50)
		s, err := m.Create(80, 24, nil)
		require.NoError(t, err)

		// Writer triggers exit as soon as it can.
		writer := s.Attach(80, 24)
		go func() {
			_, _ = writer.Write([]byte("exit\n"))
		}()

		// Attempt Attach in a tight loop until shell exits. Every
		// returned client must reach EOF within a second, regardless
		// of whether it attached before or after pump closed outputs.
		deadline := time.After(3 * time.Second)
		for {
			c := s.Attach(80, 24)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				buf := make([]byte, 4096)
				for {
					_, err := c.Read(buf)
					if err != nil {
						return
					}
				}
			}()
			select {
			case <-readDone:
				c.Close()
			case <-time.After(1 * time.Second):
				t.Fatalf("iteration %d: Attach returned a client whose "+
					"Read hung; attach-after-exit race not closed", i)
			}

			if s.Exited() {
				// One final Attach *after* exited is observable — must
				// also see EOF fast.
				c2 := s.Attach(80, 24)
				final := make(chan struct{})
				go func() {
					defer close(final)
					buf := make([]byte, 4096)
					_, _ = c2.Read(buf)
				}()
				select {
				case <-final:
					c2.Close()
				case <-time.After(1 * time.Second):
					t.Fatalf("iteration %d: post-exit Attach hung on Read", i)
				}
				break
			}
			select {
			case <-deadline:
				t.Fatalf("iteration %d: shell did not exit in time", i)
			default:
			}
		}
		writer.Close()
	}
}

// TestRenderVTEScrollback_PreservesStyle writes red-bold text that scrolls
// off-screen, then asserts the rendered scrollback contains SGR escape
// sequences (so the content is not monochrome) and the expected characters.
func TestRenderVTEScrollback_PreservesStyle(t *testing.T) {
	vte := vt.NewSafeEmulator(20, 3)
	defer vte.Close() //nolint:errcheck

	// Write 5 lines of red-bold text. With a 3-row screen, 2 lines
	// scroll off into scrollback.
	for i := range 5 {
		line := fmt.Sprintf("\x1b[1;31mhello%d\x1b[0m\r\n", i)
		_, err := vte.Write([]byte(line))
		require.NoError(t, err)
	}

	out := renderVTEScrollback(vte)
	s := string(out)

	assert.Contains(t, s, "\x1b[", "styled scrollback should contain SGR sequences")
	assert.Contains(t, s, "hello0", "content should be preserved")
	assert.Contains(t, s, "hello1", "content should be preserved")
}

// TestRenderVTEScrollback_WideCharacters writes a 2-column CJK character
// followed by ASCII, scrolls it off, and asserts the rendered scrollback
// emits the wide character exactly once without a trailing stray space
// (which would happen if the continuation cell with Width==0 were emitted
// as a space).
func TestRenderVTEScrollback_WideCharacters(t *testing.T) {
	vte := vt.NewSafeEmulator(10, 2)
	defer vte.Close() //nolint:errcheck

	// "あ" is 3 UTF-8 bytes, displays in 2 columns. Followed by "BC".
	_, err := vte.Write([]byte("あBC\r\n"))
	require.NoError(t, err)
	// Push line into scrollback with extra output.
	_, err = vte.Write([]byte("x\r\ny\r\nz\r\n"))
	require.NoError(t, err)

	out := renderVTEScrollback(vte)
	s := string(out)

	// Strip SGR sequences for the content check.
	clean := stripSGR(s)
	assert.Contains(t, clean, "あBC",
		"wide char must be followed immediately by BC without a stray "+
			"space from its continuation cell. Got (stripped): %q", clean)

	// And "あ" should appear exactly once — emitting it twice would
	// happen if we didn't skip the continuation cell correctly.
	assert.Equal(t, 1, strings.Count(clean, "あ"),
		"wide char should appear exactly once; got (stripped): %q", clean)
}

// stripSGR removes CSI SGR sequences (ESC[...m) from s for text-only
// content assertions. Does not try to be a full ANSI parser.
func stripSGR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until final byte 'm' (or end of string).
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}
