package overlay

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // empty with keep: the input passes unchanged
		keep bool
	}{
		{name: "text", in: "hello wörld 🙂", keep: true},
		{name: "BS HT CR LF", in: "a\bb\tc\r\nd", keep: true},
		{name: "bare LF gets a CR", in: "a\nb\r\nc\n\nd", want: "a\r\nb\r\nc\r\n\r\nd"},
		{name: "other C0 and DEL dropped", in: "a\x07b\x0ec\x0fd\x00e\x7ff", want: "abcdef"},
		{name: "cursor movement", in: "\x1b[3;4H\x1b[2A\x1b[B\x1b[5C\x1b[D\x1b[7G\x1b[2d\x1b[E\x1b[F", keep: true},
		{name: "erasing and editing", in: "\x1b[2J\x1b[K\x1b[3X\x1b[2@\x1b[P\x1b[L\x1b[2M", keep: true},
		{name: "SGR", in: "\x1b[1;38;2;1;2;3m\x1b[4:3m\x1b[0m", keep: true},
		{name: "scroll region", in: "\x1b[2;10r\x1b[r", keep: true},
		{name: "renderer sequences", in: "x\x1b[5b\x1b[10`\x1b[2a\x1b[3e\x1b[I\x1b[Z\x1b[2S\x1b[T\x1bM\x1bD\x1bE", keep: true},
		{name: "cursor visibility and synchronized output", in: "\x1b[?25l\x1b[?2026h\x1b[?2026l\x1b[?25h", keep: true},
		{name: "alternate screen dropped", in: "\x1b[?1049hx\x1b[?1049l\x1b[?1047h\x1b[?47h", want: "x"},
		{name: "other modes dropped", in: "\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[?1004h\x1b[4h\x1b[20h\x1b[?7l\x1b[?6h\x1b[?25;1049h"},
		{name: "queries dropped", in: "\x1b[c\x1b[>c\x1b[5n\x1b[6n\x1b[?2026$p\x1b[>q\x1b]11;?\x07\x1b[?u\x1b[14t\x1bP+q544e\x1b\\"},
		{name: "keyboard protocols dropped", in: "\x1b[>1u\x1b[<u\x1b[=0;1u\x1b[>4;1m"},
		{name: "OSC dropped", in: "\x1b]0;title\x07\x1b]2;t\x1b\\\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\x1b]52;c;aGk=\x07", want: "link"},
		{name: "saved cursor dropped", in: "\x1b7\x1b8\x1b[s\x1b[u"},
		{name: "charsets dropped", in: "\x1b(0q\x1b)0\x1b(B", want: "q"},
		{name: "cursor style dropped", in: "\x1b[5 q"},
		{name: "APC dropped", in: "\x1b_Gf=24;AAAA\x1b\\"},
		{name: "resets dropped", in: "\x1bc\x1b[!p"},
		{name: "erase display keeps the scrollback", in: "\x1b[J\x1b[0J\x1b[1J\x1b[2J\x1b[3J\x1b[2;3Jx", want: "\x1b[J\x1b[0J\x1b[1J\x1b[2Jx"},
		{name: "8-bit controls and invalid UTF-8 dropped", in: "\x9b?1049h\x9b6n\x9d2;t\x07\x90q\x9c\xffx", want: "x"},
		{name: "replacement character kept", in: "a\uFFFDb", keep: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			n, err := NewFilter(&out).Write([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, len(tt.in), n)
			want := tt.want
			if tt.keep {
				want = tt.in
			}
			assert.Equal(t, want, out.String())
		})
	}
}

func TestFilterJoinsSplitWrites(t *testing.T) {
	in := "a\x1b[?1049hb\x1b[31mc\x1b]2;x\x07d€e"
	var out bytes.Buffer
	f := NewFilter(&out)
	for i := range len(in) {
		_, err := f.Write([]byte{in[i]})
		require.NoError(t, err)
	}
	assert.Equal(t, "ab\x1b[31mcd€e", out.String())
}

func TestFilterDropsAnOversizedUnfinishedSequence(t *testing.T) {
	var out bytes.Buffer
	f := NewFilter(&out)
	_, err := f.Write([]byte("\x1b]2;" + strings.Repeat("y", filterPendingMax)))
	require.NoError(t, err)
	_, err = f.Write([]byte("\x1b[31mok"))
	require.NoError(t, err)
	assert.Equal(t, "\x1b[31mok", out.String())
}
