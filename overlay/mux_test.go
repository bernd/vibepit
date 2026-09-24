package overlay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 2*time.Second, time.Millisecond, msg)
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, buf)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out reading %d bytes", n)
	}
	return string(buf)
}

func startInputMux(t *testing.T) (*io.PipeWriter, *syncBuffer, *inputMux, chan error) {
	t.Helper()
	src, srcW := io.Pipe()
	sink := &syncBuffer{}
	m := newInputMux(src, sink)
	done := make(chan error, 1)
	go func() { done <- m.pump() }()
	t.Cleanup(func() { srcW.Close() })
	return srcW, sink, m, done
}

func TestInputMux_Routing(t *testing.T) {
	srcW, sink, m, _ := startInputMux(t)

	_, _ = srcW.Write([]byte("ls\r"))
	waitFor(t, func() bool { return sink.String() == "ls\r" }, "default sink gets input")

	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	go func() { _, _ = srcW.Write([]byte("a")) }()
	assert.Equal(t, "a", readN(t, r, 1), "holder gets input")
	assert.Equal(t, "ls\r", sink.String(), "session gets nothing while acquired")

	release()
	release() // idempotent
	_, _ = srcW.Write([]byte("pwd\r"))
	waitFor(t, func() bool { return sink.String() == "ls\rpwd\r" }, "input goes back to the session")

	_, err = r.Read(make([]byte, 1))
	assert.Error(t, err, "released reader is closed")
}

func TestInputMux_ChunksAreNotSplit(t *testing.T) {
	srcW, sink, m, _ := startInputMux(t)

	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	go func() { _, _ = srcW.Write([]byte("\x1b[A")) }()
	assert.Equal(t, "\x1b[A", readN(t, r, 3))
	release()
	assert.Empty(t, sink.String())
}

func TestInputMux_ReleaseUnblocksPump(t *testing.T) {
	srcW, sink, m, _ := startInputMux(t)

	_, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	// Nobody reads the overlay's input: the pump goes on regardless.
	_, _ = srcW.Write([]byte("x"))
	time.Sleep(20 * time.Millisecond)
	release()
	// Input the overlay did not take before it closed goes to the session,
	// and the pump carries on.
	_, _ = srcW.Write([]byte("y"))
	waitFor(t, func() bool { return sink.String() == "xy" }, "pump continues after release")
}

func TestInputMux_ReportsGoToSession(t *testing.T) {
	// While an overlay holds the input, the terminal's replies to the
	// session's queries and its focus reports still reach the session.
	// Everything the user does goes to the overlay.
	srcW, sink, m, _ := startInputMux(t)
	m.cprReply = func() bool { return true } // the session asked for the cursor position
	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	var owner syncBuffer
	go func() { _, _ = io.Copy(&owner, r) }()

	replies := []string{"\x1b[12;40R", "\x1b[?62;22c", "\x1b[?2026;2$y", "\x1b]11;rgb:0/0/0\x1b\\", "\x1b[?1u", "\x1b[0n", "\x1b[I"}
	user := []string{"all good\r", "\x1b[A", "\x1b[<35;10;5M", "\x1b[200~paste\x1b[201~", "\x1bx", "q", "\x1b"}
	var in string
	for i := range replies {
		in += replies[i] + user[i]
	}
	_, _ = srcW.Write([]byte(in))
	waitFor(t, func() bool {
		return owner.String() == strings.Join(user, "") && sink.String() == strings.Join(replies, "")
	}, "routed")
}

func TestSplitInput(t *testing.T) {
	type unit struct {
		s    string
		kind unitKind
	}
	tests := []struct {
		in         string
		cpr        bool
		want       []unit
		rest       string
		restReport bool
	}{
		{in: "ab", want: []unit{{"a", unitUser}, {"b", unitUser}}},
		{in: "é", want: []unit{{"é", unitUser}}},
		{in: "\r\x7f\x03", want: []unit{{"\r", unitUser}, {"\x7f", unitUser}, {"\x03", unitUser}}},
		{in: "\x1b[A\x1b[1;5C\x1b[3~\x1b[97;5u", want: []unit{{"\x1b[A", unitUser}, {"\x1b[1;5C", unitUser}, {"\x1b[3~", unitUser}, {"\x1b[97;5u", unitUser}}},
		{in: "\x1b[<0;3;4M", want: []unit{{"\x1b[<0;3;4M", unitUser}}},
		{in: "\x1b[>1;2c", want: []unit{{"\x1b[>1;2c", unitReport}}},
		{in: "\x1bP>|kitty\x1b\\", want: []unit{{"\x1bP>|kitty\x1b\\", unitReport}}},
		{in: "\x1b[O", want: []unit{{"\x1b[O", unitReport}}},
		{in: "\x1b[1;5O", want: []unit{{"\x1b[1;5O", unitUser}}},
		{in: "\x1b[1;2R", want: []unit{{"\x1b[1;2R", unitUser}}},
		{in: "\x1b[1;2R", cpr: true, want: []unit{{"\x1b[1;2R", unitReport}}},
		{in: "\x1b[?5;1;1R", want: []unit{{"\x1b[?5;1;1R", unitReport}}},
		{in: "a\x1b", want: []unit{{"a", unitUser}}, rest: "\x1b"},
		{in: "\x1b]", rest: "\x1b]"},
		{in: "\x1b]11;rgb:1", rest: "\x1b]11;rgb:1", restReport: true},
		{in: "\x1bP+r544e", rest: "\x1bP+r544e", restReport: true},
		{in: "\x1b[?62;2", rest: "\x1b[?62;2", restReport: true},
		{in: "\x1b[12;4", rest: "\x1b[12;4"},
		{in: "\x1b_Gi=1;OK\x1b\\", want: []unit{{"\x1b_Gi=1;OK\x1b\\", unitReport}}},
		{in: "\x1bP1$r0m\x1b\\", want: []unit{{"\x1bP1$r0m\x1b\\", unitReport}}},
		{in: "\x1bP1$", rest: "\x1bP1$", restReport: true},
		// Alt+_, Alt+], Alt+X, Alt+P and what the user types after them.
		{in: "\x1b_ab", want: []unit{{"\x1b_a", unitUser}, {"b", unitUser}}},
		{in: "\x1b]a", want: []unit{{"\x1b]a", unitUser}}},
		{in: "\x1b]1x", want: []unit{{"\x1b]1x", unitUser}}},
		{in: "\x1bXa", want: []unit{{"\x1bX", unitUser}, {"a", unitUser}}},
		{in: "\x1bPq", want: []unit{{"\x1bPq", unitUser}}},
		{in: "\x1b]\x07", want: []unit{{"\x1b]\x07", unitUser}}},
		{in: "\x1b[1;2R\x1b[1;2R", cpr: true, want: []unit{{"\x1b[1;2R", unitReport}, {"\x1b[1;2R", unitReport}}},
		{in: "\xe2\x94", rest: "\xe2\x94"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			var got []unit
			cpr := func() bool { return tt.cpr }
			rest, restReport := splitInput([]byte(tt.in), cpr, func(u []byte, k unitKind) { got = append(got, unit{string(u), k}) })
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.rest, string(rest))
			assert.Equal(t, tt.restReport, restReport)
		})
	}
}

func TestInputMux_SplitReads(t *testing.T) {
	tests := []struct {
		name         string
		reads        []string
		wantOwner    string
		wantSession  string
		afterTimeout bool // the owner part arrives only after escTimeout
	}{
		{
			name:        "a reply split across reads reaches the session",
			reads:       []string{"\x1b]11;rgb:", "0/0/0\x1b\\"},
			wantSession: "\x1b]11;rgb:0/0/0\x1b\\",
		},
		{
			name:        "a CSI reply split right after ESC",
			reads:       []string{"\x1b", "[?62;22c"},
			wantSession: "\x1b[?62;22c",
		},
		{
			name:         "a lone Esc reaches the overlay after the timeout",
			reads:        []string{"\x1b"},
			wantOwner:    "\x1b",
			afterTimeout: true,
		},
		{
			name:        "a stuck reply goes to the session after the timeout",
			reads:       []string{"\x1b]11;rgb:0/0"},
			wantSession: "\x1b]11;rgb:0/0",
		},
		{
			name:      "a character split across reads",
			reads:     []string{"\xe2\x94", "\x80"},
			wantOwner: "─",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcW, sink, m, _ := startInputMux(t)
			r, release, err := m.Acquire(context.Background())
			require.NoError(t, err)
			defer release()
			var owner syncBuffer
			go func() { _, _ = io.Copy(&owner, r) }()

			start := time.Now()
			for _, in := range tt.reads {
				_, _ = srcW.Write([]byte(in))
				time.Sleep(20 * time.Millisecond)
			}
			waitFor(t, func() bool { return owner.String() == tt.wantOwner && sink.String() == tt.wantSession }, "routed")
			if tt.afterTimeout {
				assert.GreaterOrEqual(t, time.Since(start), escTimeout)
			}
		})
	}
}

func TestInputMux_StuckSession(t *testing.T) {
	// The session stopped reading its input, e.g. stuck writing output the
	// overlay holds back. The overlay must still get its input.
	src, srcW := io.Pipe()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	session := writerFunc(func(p []byte) (int, error) {
		<-stuck
		return len(p), nil
	})
	m := newInputMux(src, session)
	go func() { _ = m.pump() }()
	t.Cleanup(func() { srcW.Close() })

	_, _ = srcW.Write([]byte(strings.Repeat("big paste ", 1000)))
	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	go func() { _, _ = srcW.Write([]byte("\x1b[0n")) }()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = srcW.Write([]byte("a"))
	}()
	assert.Equal(t, "a", readN(t, r, 1), "the overlay gets its key")
}

func TestInputMux_AltStringKeysGoToOverlay(t *testing.T) {
	// Alt+_ starts what looks like an APC string. The keys typed after it
	// are the user's and reach the overlay right away, not after a pause.
	srcW, sink, m, _ := startInputMux(t)
	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	var owner syncBuffer
	go func() { _, _ = io.Copy(&owner, r) }()

	start := time.Now()
	_, _ = srcW.Write([]byte("\x1b_"))
	_, _ = srcW.Write([]byte("a"))
	waitFor(t, func() bool { return owner.String() == "\x1b_a" }, "keys reach the overlay")
	assert.Less(t, time.Since(start), escTimeout)
	assert.Empty(t, sink.String())
}

func TestInputMux_SplitAcrossAcquire(t *testing.T) {
	// A reply the session got the start of before the overlay took the
	// input gets its rest, which is not typing for the overlay.
	srcW, sink, m, _ := startInputMux(t)
	_, _ = srcW.Write([]byte("\x1b[?6"))
	waitFor(t, func() bool { return sink.String() == "\x1b[?6" }, "start delivered")
	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	var owner syncBuffer
	go func() { _, _ = io.Copy(&owner, r) }()

	_, _ = srcW.Write([]byte("2;22c"))
	_, _ = srcW.Write([]byte("a"))
	waitFor(t, func() bool { return sink.String() == "\x1b[?62;22c" && owner.String() == "a" }, "routed")
}

func TestInputMux_StaleStartIsNotContinued(t *testing.T) {
	// A lone Esc typed for the session long before the overlay took the
	// input does not claim the next key.
	srcW, sink, m, _ := startInputMux(t)
	_, _ = srcW.Write([]byte("\x1b"))
	waitFor(t, func() bool { return sink.String() == "\x1b" }, "delivered")
	time.Sleep(escTimeout)
	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	go func() { _, _ = srcW.Write([]byte("a")) }()
	assert.Equal(t, "a", readN(t, r, 1))
}

func TestInputMux_UnreadInputGoesToSession(t *testing.T) {
	// The program quit: what it did not read is typing meant for the
	// session, including a read already waiting when it stopped.
	srcW, sink, m, _ := startInputMux(t)
	in, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	readDone := make(chan error, 1)
	go func() {
		_, err := in.Read(make([]byte, 16))
		readDone <- err
	}()
	time.Sleep(10 * time.Millisecond)
	in.stop()
	_, _ = srcW.Write([]byte("ls"))
	time.Sleep(10 * time.Millisecond)
	assert.Empty(t, sink.String(), "held until release")
	release()
	waitFor(t, func() bool { return sink.String() == "ls" }, "unread input reaches the session")
	select {
	case err := <-readDone:
		assert.ErrorIs(t, err, io.ErrClosedPipe, "the waiting read got nothing")
	case <-time.After(2 * time.Second):
		t.Fatal("waiting read not released")
	}
}

func TestInputMux_PartialKeepsOrder(t *testing.T) {
	// An Esc held for its timeout and the key after it reach the overlay
	// in the order typed.
	for range 20 {
		srcW, _, m, _ := startInputMux(t)
		r, release, err := m.Acquire(context.Background())
		require.NoError(t, err)
		_, _ = srcW.Write([]byte("\x1b"))
		time.Sleep(escTimeout)
		_, _ = srcW.Write([]byte("a"))
		assert.Equal(t, "\x1ba", readN(t, r, 2))
		release()
	}
}

func TestQueueWriter(t *testing.T) {
	gate := make(chan struct{})
	var got syncBuffer
	w := writerFunc(func(p []byte) (int, error) {
		<-gate
		return got.Write(p)
	})
	q := newQueueWriter(w, 4)

	_, err := q.Write([]byte("abcd")) // taken by the writer, which waits
	require.NoError(t, err)
	waitFor(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.buf) == 0
	}, "handed to the writer")
	_, err = q.Write([]byte("efgh")) // fills the queue
	require.NoError(t, err)

	wrote := make(chan struct{})
	go func() {
		_, _ = q.Write([]byte("ij"))
		close(wrote)
	}()
	select {
	case <-wrote:
		t.Fatal("Write must wait while the queue is full")
	case <-time.After(20 * time.Millisecond):
	}
	q.setNoWait(true)
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("Write must not wait with noWait")
	}
	require.NoError(t, q.push([]byte("k")), "push never waits")

	close(gate)
	require.NoError(t, q.Close())
	assert.Equal(t, "abcdefghijk", got.String())
	_, err = q.Write([]byte("late"))
	assert.ErrorIs(t, err, io.ErrClosedPipe, "writes after Close fail")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestInputMux_AcquireWaitsForQuiet(t *testing.T) {
	srcW, sink, m, _ := startInputMux(t)

	_, _ = srcW.Write([]byte("k"))
	waitFor(t, func() bool { return sink.String() == "k" }, "delivered")
	start := time.Now()
	_, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	assert.GreaterOrEqual(t, time.Since(start), inputQuiet/2)
	assert.Equal(t, "k", sink.String())
}

func TestInputMux_EOF(t *testing.T) {
	srcW, _, m, done := startInputMux(t)

	r, release, err := m.Acquire(context.Background())
	require.NoError(t, err)
	defer release()
	srcW.Close()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on EOF")
	}
	_, err = r.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "holder sees EOF")
}

func startOutputMux(t *testing.T) (*io.PipeWriter, *syncBuffer, *outputMux, *modeTracker, chan error) {
	t.Helper()
	src, srcW := io.Pipe()
	dst := &syncBuffer{}
	tr := newModeTracker()
	m := newOutputMux(src, dst, tr)
	m.pauseTimeout = 400 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- m.pump() }()
	t.Cleanup(func() { srcW.Close() })
	return srcW, dst, m, tr, done
}

func TestOutputMux_PauseAndReplay(t *testing.T) {
	srcW, dst, m, tr, _ := startOutputMux(t)

	_, _ = srcW.Write([]byte("one "))
	waitFor(t, func() bool { return dst.String() == "one " }, "delivered")
	_, resume, err := m.Pause(context.Background())
	require.NoError(t, err)

	_, _ = srcW.Write([]byte("two \x1b[?25l"))
	_, _ = srcW.Write([]byte("three"))
	assert.Equal(t, "one ", dst.String(), "paused output is held back")
	assert.False(t, tr.Snapshot().CursorVisible, "tracker sees buffered bytes")

	require.NoError(t, resume())
	// The last chunk may still be on its way through the pump.
	waitFor(t, func() bool { return dst.String() == "one two \x1b[?25lthree" }, "replayed in order")
	require.NoError(t, resume(), "resume is idempotent")

	_, _ = srcW.Write([]byte(" four"))
	waitFor(t, func() bool { return dst.String() == "one two \x1b[?25lthree four" }, "passes through again")
}

func TestOutputMux_PauseWaitsForGround(t *testing.T) {
	srcW, dst, m, _, _ := startOutputMux(t)

	_, _ = srcW.Write([]byte("\x1b[?10"))
	waitFor(t, func() bool { return dst.String() == "\x1b[?10" }, "delivered")
	type result struct {
		resume func() error
		err    error
	}
	paused := make(chan result, 1)
	go func() {
		_, r, err := m.Pause(context.Background())
		paused <- result{r, err}
	}()
	select {
	case <-paused:
		t.Fatal("Pause must wait inside an escape sequence")
	case <-time.After(50 * time.Millisecond):
	}
	_, _ = srcW.Write([]byte("49h"))
	var res result
	select {
	case res = <-paused:
	case <-time.After(time.Second):
		t.Fatal("Pause did not return at the end of the sequence")
	}
	require.NoError(t, res.err)
	assert.Equal(t, "\x1b[?1049h", dst.String(), "the completing chunk passes through")
	require.NoError(t, res.resume())
}

func TestOutputMux_PauseWaitsForSyncBatch(t *testing.T) {
	srcW, dst, m, _, _ := startOutputMux(t)

	_, _ = srcW.Write([]byte("\x1b[?2026hframe"))
	waitFor(t, func() bool { return dst.String() == "\x1b[?2026hframe" }, "delivered")
	paused := make(chan func() error, 1)
	go func() {
		_, r, _ := m.Pause(context.Background())
		paused <- r
	}()
	time.Sleep(20 * time.Millisecond)
	_, _ = srcW.Write([]byte("\x1b[?2026l"))
	resume := <-paused
	assert.Equal(t, "\x1b[?2026hframe\x1b[?2026l", dst.String())
	require.NoError(t, resume())
}

func TestOutputMux_PauseCutAt(t *testing.T) {
	// The cut reports the state at the cut, not later buffered output: a
	// screen switch after the cut must not make the overlay think it is
	// on the alternate screen.
	srcW, dst, m, tr, _ := startOutputMux(t)

	_, _ = srcW.Write([]byte("\x1b[?25"))
	waitFor(t, func() bool { return dst.String() == "\x1b[?25" }, "delivered")
	type result struct {
		cut    cutState
		resume func() error
	}
	paused := make(chan result, 1)
	go func() {
		c, r, err := m.Pause(context.Background())
		assert.NoError(t, err)
		paused <- result{c, r}
	}()
	time.Sleep(20 * time.Millisecond)
	_, _ = srcW.Write([]byte("l"))
	_, _ = srcW.Write([]byte("\x1b[?1049h"))
	res := <-paused
	waitFor(t, func() bool { return tr.Snapshot().AltScreen }, "switch buffered and tracked")

	assert.False(t, res.cut.AltScreen, "cut is on the main screen")
	assert.False(t, res.cut.CursorVisible)
	assert.False(t, res.cut.Aborted)
	assert.Equal(t, "\x1b[?25l", dst.String())
	require.NoError(t, res.resume())
	assert.Equal(t, "\x1b[?25l\x1b[?1049h", dst.String())
}

var uncleanPoints = []struct {
	name    string
	before  string
	finish  string
	aborted bool
	sync    bool
}{
	{name: "unfinished CSI", before: "shell\x1b[31", finish: "m", aborted: true},
	{name: "control inside CSI", before: "shell\x1b[31\n", finish: "m", aborted: true},
	{name: "unfinished OSC", before: "\x1b]0;title", finish: "\a", aborted: true},
	{name: "long OSC", before: "\x1b]52;c;" + strings.Repeat("A", 8192), finish: "\a", aborted: true},
	{name: "open sync batch", before: "\x1b[?2026hframe", finish: "\x1b[?2026l", sync: true},
	{name: "partial UTF-8", before: "\xe2\x94", finish: "\x80", aborted: true},
	{name: "fresh cursor save", before: "\x1b7progress", finish: "\x1b8"},
}

func TestOutputMux_PauseWaitsForCleanPoint(t *testing.T) {
	for _, tt := range uncleanPoints {
		t.Run(tt.name, func(t *testing.T) {
			srcW, dst, m, tr, _ := startOutputMux(t)
			// Keep a cursor save fresh for the whole test.
			now := time.Now()
			tr.now = func() time.Time { return now }

			_, _ = srcW.Write([]byte(tt.before))
			waitFor(t, func() bool { return dst.String() == tt.before }, "delivered")
			paused := make(chan cutState, 1)
			resumes := make(chan func() error, 1)
			go func() {
				c, r, err := m.Pause(context.Background())
				assert.NoError(t, err)
				paused <- c
				resumes <- r
			}()
			select {
			case <-paused:
				t.Fatal("Pause must not cut at an unclean point")
			case <-time.After(m.pauseTimeout / 4):
			}
			_, _ = srcW.Write([]byte(tt.finish))
			select {
			case c := <-paused:
				assert.False(t, c.Aborted, "clean cut")
				assert.False(t, c.SyncOutput, "clean cut")
			case <-time.After(m.pauseTimeout / 2):
				t.Fatal("Pause did not cut once the stream got back to a clean point")
			}
			assert.Equal(t, tt.before+tt.finish, dst.String(), "the completing chunk passes through")
			require.NoError(t, (<-resumes)())
		})
	}
}

func TestOutputMux_PauseCutsAfterTimeout(t *testing.T) {
	// An app killed inside a sequence or batch never finishes it. Pause
	// must not block prompts for good.
	for _, tt := range uncleanPoints {
		t.Run(tt.name, func(t *testing.T) {
			srcW, dst, m, tr, _ := startOutputMux(t)
			now := time.Now()
			tr.now = func() time.Time { return now }

			_, _ = srcW.Write([]byte(tt.before))
			waitFor(t, func() bool { return dst.String() == tt.before }, "delivered")
			start := time.Now()
			c, resume, err := m.Pause(context.Background())
			require.NoError(t, err)
			assert.GreaterOrEqual(t, time.Since(start), m.pauseTimeout)
			assert.Equal(t, tt.aborted, c.Aborted)
			assert.Equal(t, tt.sync, c.SyncOutput)
			require.NoError(t, resume())
		})
	}
}

func TestOutputMux_PauseAfterSaveHold(t *testing.T) {
	// A cursor save that is never restored delays the cut only briefly,
	// even without further output.
	srcW, dst, m, _, _ := startOutputMux(t)
	_, _ = srcW.Write([]byte("\x1b7"))
	waitFor(t, func() bool { return dst.String() == "\x1b7" }, "delivered")
	start := time.Now()
	c, resume, err := m.Pause(context.Background())
	require.NoError(t, err)
	assert.Less(t, time.Since(start), m.pauseTimeout/2)
	assert.False(t, c.Aborted)
	require.NoError(t, resume())
}

func TestOutputMux_PauseCancelled(t *testing.T) {
	srcW, dst, m, _, _ := startOutputMux(t)

	_, _ = srcW.Write([]byte("\x1b["))
	waitFor(t, func() bool { return dst.String() == "\x1b[" }, "delivered")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := m.Pause(ctx)
	assert.ErrorIs(t, err, context.Canceled)

	_, _ = srcW.Write([]byte("m"))
	waitFor(t, func() bool { return dst.String() == "\x1b[m" }, "delivered")
	_, resume, err := m.Pause(context.Background())
	require.NoError(t, err, "a cancelled Pause leaves the mux usable")
	require.NoError(t, resume())
}

func TestOutputMux_BufferCapStalls(t *testing.T) {
	srcW, dst, m, _, _ := startOutputMux(t)

	_, resume, err := m.Pause(context.Background())
	require.NoError(t, err)

	chunk := bytes.Repeat([]byte("x"), chunkSize)
	wrote := make(chan int, 1)
	go func() {
		n := 0
		for n < 2*maxBuffered {
			w, err := srcW.Write(chunk)
			n += w
			if err != nil {
				break
			}
		}
		wrote <- n
	}()
	select {
	case <-wrote:
		t.Fatal("writer must stall once the buffer is full")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, resume())
	select {
	case n := <-wrote:
		assert.Equal(t, 2*maxBuffered, n)
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not continue after resume")
	}
	waitFor(t, func() bool { return len(dst.String()) == 2*maxBuffered }, "all output delivered")
}

func TestOutputMux_ResumeAfterEOF(t *testing.T) {
	srcW, dst, m, _, done := startOutputMux(t)

	_, resume, err := m.Pause(context.Background())
	require.NoError(t, err)
	_, _ = srcW.Write([]byte("bye"))
	srcW.Close()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on EOF")
	}
	require.NoError(t, resume())
	assert.Equal(t, "bye", dst.String())
}
