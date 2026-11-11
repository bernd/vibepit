package ghostty

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests describe what the formatter does NOT do today. When an
// upgrade makes one fail, upstream closed a gap: drop vibepit's own
// emission of that state so it isn't emitted twice.
func TestKnownGaps(t *testing.T) {
	full := func(t *testing.T, in *Instance, extra TerminalExtra) string {
		t.Helper()
		return format(t, in, FormatterOptions{Emit: FormatVT, Extra: extra})
	}

	t.Run("a link open at the cut is reopened as another link", func(t *testing.T) {
		// The cells and the pen share the link, but the content closes it
		// and the hyperlink extra opens it again. Without an explicit id
		// the replay gives each OSC 8 a link of its own.
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]8;;http://x\x1b\\li")
		got := full(t, in, restoreExtras)
		assert.Contains(t, got, "\x1b]8;;http://x\x1b\\li\x1b]8;;\x1b\\")
		assert.True(t, strings.HasSuffix(got, "\x1b]8;;http://x\x1b\\"), "%q", got)
		assert.NotContains(t, got, "id=")
	})
	t.Run("no DECSCUSR cursor shape", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b[5 q")
		assert.NotContains(t, full(t, in, restoreExtras), " q")
	})
	t.Run("no title", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]2;TITLE\x07")
		assert.NotContains(t, full(t, in, restoreExtras), "TITLE")
	})
	t.Run("pwd extra emits a stray NUL", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]7;file://h/tmp\x07")
		extra := restoreExtras
		extra.Pwd = true
		assert.Contains(t, full(t, in, extra), "\x1b]7;file://h/tmp\x00")
	})
	t.Run("only the active screen", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "SHELL\x1b[?1049hVIM")
		assert.NotContains(t, full(t, in, restoreExtras), "SHELL")
	})
	t.Run("no saved cursor", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "ab\x1b7\x1b[3;3H")
		got := full(t, in, restoreExtras)
		assert.NotContains(t, got, "\x1b7")
		assert.NotContains(t, got, "\x1b[?1048h")
	})
	t.Run("tab-stop extra moves the cursor", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b[3;5Hx")
		got := format(t, in, FormatterOptions{Emit: FormatVT, ContentNone: true, Extra: TerminalExtra{Tabstops: true}})
		assert.Contains(t, got, "\x1b[3g")
		assert.Regexp(t, regexp.MustCompile(`\x1b\[\d+G\x1bH`), got)
	})
}
