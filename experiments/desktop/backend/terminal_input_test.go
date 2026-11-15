package backend

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalBinaryInput(t *testing.T) {
	input := &bufferCloser{}
	s := &Service{conn: &connection{id: "current", input: input}}
	want := []byte{0x1b, '[', 'M', 32, 200, 255}
	encoded := base64.StdEncoding.EncodeToString(want)
	require.NoError(t, s.WriteTerminalBytes("current", encoded))
	assert.Equal(t, want, input.Bytes())
	require.Error(t, s.WriteTerminalBytes("current", "not base64!"))
	require.Error(t, s.WriteTerminalBytes("stale", encoded))
	assert.Equal(t, want, input.Bytes())
}

func TestTerminalBinaryInputWorkspaceScope(t *testing.T) {
	var events []Event
	r := NewWorkspaces(true, func(e Event) { events = append(events, e) }, t.TempDir())
	defer r.Shutdown()
	first, err := r.NewWindow()
	require.NoError(t, err)
	second, err := r.NewWindow()
	require.NoError(t, err)
	token, err := r.Connect(first.ID, "demo")
	require.NoError(t, err)
	require.Error(t, r.WriteTerminalBytes(second.ID, token, "G1tNIMj/"))
	require.NoError(t, r.WriteTerminalBytes(first.ID, token, "G1tNIMj/"))
	require.Len(t, events, 1)
	assert.Equal(t, first.ID, events[0].Workspace)
	assert.Equal(t, "G1tNIMj/", events[0].Data)
}

func TestDemoTerminalShellReports(t *testing.T) {
	var output []byte
	c := &connection{id: "demo", emit: func(e Event) {
		if e.Kind != "terminal" {
			return
		}
		data, err := base64.StdEncoding.DecodeString(e.Data.(string))
		require.NoError(t, err)
		output = append(output, data...)
	}}
	c.active.Store(true)
	s := &Service{demo: true, conn: c}
	require.NoError(t, s.OpenTerminal("demo", 80, 24))
	assert.Contains(t, string(output), "\x1b]7;file://vibepit/home/code/project\x07")
	assert.Contains(t, string(output), "\x1b]133;A\x07")
	assert.Contains(t, string(output), "\x1b]133;B\x07")
	output = nil
	require.NoError(t, s.WriteTerminal("demo", "\r"))
	assert.Contains(t, string(output), "\x1b]133;C\x07")
	assert.Contains(t, string(output), "\x1b]133;D;0\x07")
}
