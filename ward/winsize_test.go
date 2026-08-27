package ward

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"sync/atomic"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWinsizeRewriter(t *testing.T) {
	tests := []struct {
		name   string
		rows   int
		writes []string
		want   string
	}{
		{
			name:   "cell size reply loses one row",
			rows:   48,
			writes: []string{"\x1b[8;48;97t"},
			want:   "\x1b[8;47;97t",
		},
		{
			name:   "pixel size reply scaled by one row",
			rows:   48,
			writes: []string{"\x1b[4;960;1940t"},
			want:   "\x1b[4;940;1940t",
		},
		{
			name:   "reply split across writes",
			rows:   48,
			writes: []string{"abc\x1b[8;4", "8;97tdef"},
			want:   "abc\x1b[8;47;97tdef",
		},
		{
			name:   "other CSI passes through",
			rows:   48,
			writes: []string{"\x1b[A\x1b[8~\x1b[4;2H"},
			want:   "\x1b[A\x1b[8~\x1b[4;2H",
		},
		{
			name:   "multiple replies in one chunk",
			rows:   24,
			writes: []string{"\x1b[8;24;80t\x1b[4;480;800t"},
			want:   "\x1b[8;23;80t\x1b[4;460;800t",
		},
		{
			name:   "screen size reply loses one row",
			rows:   48,
			writes: []string{"\x1b[9;48;97t"},
			want:   "\x1b[9;47;97t",
		},
		{
			name:   "in-band resize notification adjusted",
			rows:   48,
			writes: []string{"\x1b[48;48;97;960;1940t"},
			want:   "\x1b[48;47;97;940;1940t",
		},
		{
			name:   "in-band resize split mid-kind",
			rows:   48,
			writes: []string{"\x1b[4", "8;48;97;960;1940t"},
			want:   "\x1b[48;47;97;940;1940t",
		},
		{
			name:   "unrelated t sequences pass through",
			rows:   48,
			writes: []string{"\x1b[22;0;0t\x1b[1t\x1b[48t\x1b[t\x1b[8;;97t\x1b[?8;48;97t"},
			want:   "\x1b[22;0;0t\x1b[1t\x1b[48t\x1b[t\x1b[8;;97t\x1b[?8;48;97t",
		},
		{
			name:   "overlong sequence passes through",
			rows:   48,
			writes: []string{"\x1b[8;4", "8888888888888888888888888888888888888;97t"},
			want:   "\x1b[8;48888888888888888888888888888888888888;97t",
		},
		{
			name:   "single row terminal untouched",
			rows:   1,
			writes: []string{"\x1b[8;1;80t"},
			want:   "\x1b[8;1;80t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			r := newWinsizeRewriter(&out, atomicRows(tt.rows))
			for _, w := range tt.writes {
				n, err := r.Write([]byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n)
			}
			assert.Equal(t, tt.want, out.String())
		})
	}
}

func atomicRows(n int) *atomic.Int32 {
	var v atomic.Int32
	v.Store(int32(n))
	return &v
}

func TestWinsizeRewriterLoneEscapePassesThrough(t *testing.T) {
	var out bytes.Buffer
	r := newWinsizeRewriter(&out, atomicRows(48))

	_, err := r.Write([]byte("\x1b"))
	require.NoError(t, err)
	assert.Equal(t, "\x1b", out.String(), "lone ESC must not be delayed")
}

func TestWinsizeRewriterFlushesHeldPrefix(t *testing.T) {
	var out bytes.Buffer
	r := newWinsizeRewriter(&out, atomicRows(48))

	_, err := r.Write([]byte("\x1b[8;4"))
	require.NoError(t, err)
	assert.Empty(t, out.String(), "size-report prefix should be held briefly")

	assert.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return out.String() == "\x1b[8;4"
	}, time.Second, 5*time.Millisecond)
}

// recordingWriter keeps each Write call separate so tests can assert on
// write boundaries, not just the concatenated output.
type recordingWriter struct {
	chunks []string
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.chunks = append(w.chunks, string(p))
	return len(p), nil
}

func TestWinsizeRewriterKeepsSequencesWhole(t *testing.T) {
	var out recordingWriter
	r := newWinsizeRewriter(&out, atomicRows(48))

	_, err := r.Write([]byte("\x1b[A\x1b[Bxyz"))
	require.NoError(t, err)
	for _, c := range out.chunks {
		assert.NotEqual(t, "\x1b", c, "a lone ESC must not be split off from its sequence: %q", out.chunks)
	}
	assert.Equal(t, "\x1b[A\x1b[Bxyz", strings.Join(out.chunks, ""))
}

func TestWinsizeRewriterDoesNotHoldNonReportPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"OSC colour reply", "\x1b]11;rgb:1e1e"},
		{"DCS reply", "\x1bP1+r"},
		{"split SGR mouse report", "\x1b[<0;10;2"},
		{"private CSI prefix", "\x1b[?8;4"},
		{"alt-bracket-close", "\x1b]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			r := newWinsizeRewriter(&out, atomicRows(48))
			_, err := r.Write([]byte(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.input, out.String(), "cannot become a size report; must not be delayed")
		})
	}
}

func TestWinsizeRewriterRejectsOutOfRangeParams(t *testing.T) {
	tests := []string{
		"\x1b[8;18446744073709551640;97t", // wraps past int64 back into range
		"\x1b[8;65536;97t",                // above the parser's MaxParam
	}
	for _, in := range tests {
		var out bytes.Buffer
		r := newWinsizeRewriter(&out, atomicRows(48))
		_, err := r.Write([]byte(in))
		require.NoError(t, err)
		assert.Equal(t, in, out.String())
	}
}

func TestWinsizeRewriterInBandResizeUsesReportedRows(t *testing.T) {
	var out bytes.Buffer
	// ward's own row count lags behind the report during a resize.
	r := newWinsizeRewriter(&out, atomicRows(24))
	_, err := r.Write([]byte("\x1b[48;48;97;960;1940t"))
	require.NoError(t, err)
	assert.Equal(t, "\x1b[48;47;97;940;1940t", out.String())
}

func TestWinsizeRewriterStaleFlushIsIgnored(t *testing.T) {
	var out bytes.Buffer
	r := newWinsizeRewriter(&out, atomicRows(48))

	_, err := r.Write([]byte("\x1b[8;4"))
	require.NoError(t, err)
	r.mu.Lock()
	stale := r.gen
	r.mu.Unlock()

	_, err = r.Write([]byte("8;9"))
	require.NoError(t, err)

	// A timer that fired for the first prefix must not release the second.
	r.flushPending(stale)
	assert.Empty(t, out.String())

	_, err = r.Write([]byte("7t"))
	require.NoError(t, err)
	assert.Equal(t, "\x1b[8;47;97t", out.String())
}

// failAfterWriter succeeds for the first ok calls, then fails.
type failAfterWriter struct {
	ok    int
	calls int
}

var errWrite = errors.New("pty closed")

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.ok {
		return 0, errWrite
	}
	return len(p), nil
}

func TestWinsizeRewriterReportsBytesConsumedOnError(t *testing.T) {
	r := newWinsizeRewriter(&failAfterWriter{ok: 1}, atomicRows(48))
	n, err := r.Write([]byte("abc\x1b[A"))
	require.ErrorIs(t, err, errWrite)
	assert.Equal(t, 3, n)
}

func TestWinsizeRewriterDiscardsPendingOnError(t *testing.T) {
	r := newWinsizeRewriter(&failAfterWriter{ok: 0}, atomicRows(48))
	_, err := r.Write([]byte("\x1b[8;4"))
	require.NoError(t, err, "held prefix does not touch the writer")
	_, err = r.Write([]byte("8;97t"))
	require.ErrorIs(t, err, errWrite)

	r.mu.Lock()
	defer r.mu.Unlock()
	assert.Zero(t, r.npend, "pending bytes must not survive a write error")
	assert.False(t, r.flush != nil && r.flush.Stop(), "flush timer must be disarmed after a write error")
}
