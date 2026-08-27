package ward

import (
	"bytes"
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
