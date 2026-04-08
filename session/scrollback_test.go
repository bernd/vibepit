package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
