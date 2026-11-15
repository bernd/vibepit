package backend

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspacesScopeConnectionsAndShutdown(t *testing.T) {
	var events []Event
	r := NewWorkspaces(true, func(e Event) { events = append(events, e) }, t.TempDir())
	first, err := r.NewWindow()
	require.NoError(t, err)
	second, err := r.NewWindow()
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	a, err := r.Connect(first.ID, "demo")
	require.NoError(t, err)
	b, err := r.Connect(second.ID, "demo")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	require.Error(t, r.WriteTerminal(first.ID, b, "wrong window"))
	require.Error(t, r.ResizeTerminal(second.ID, a, 80, 24))
	require.NoError(t, r.WriteTerminal(first.ID, a, "one"))
	require.NoError(t, r.WriteTerminal(second.ID, b, "two"))
	require.Len(t, events, 2)
	assert.Equal(t, first.ID, events[0].Workspace)
	assert.Equal(t, second.ID, events[1].Workspace)
	require.NoError(t, r.Disconnect(first.ID))
	_, err = r.StartClaude(second.ID, b, true)
	require.NoError(t, err)
	info, err := r.GetWindow(second.ID)
	require.NoError(t, err)
	assert.True(t, info.Connected)
	require.Error(t, r.WriteTerminal(first.ID, a, "stale"))
	r.Shutdown()
	_, err = r.GetWindow(second.ID)
	require.Error(t, err)
	_, err = r.NewWindow()
	require.Error(t, err)
}

func TestWorkspaceNoticeLifecycle(t *testing.T) {
	r := NewWorkspaces(true, func(Event) {}, t.TempDir())
	defer r.Shutdown()
	w, err := r.NewWindow()
	require.NoError(t, err)
	token, err := r.Connect(w.ID, "demo")
	require.NoError(t, err)
	var notices []Notice
	r.OnNotice = func(n Notice) { notices = append(notices, n); _, _ = r.GetWindow(w.ID) }
	r.OnChange = func() { _ = r.ListWindows() }
	send := func(kind string, data any) { r.receive(w.ID, Event{Connection: token, Kind: kind, Data: data}) }
	hold := proxy.LogEntry{ID: 1, Domain: "example.com", Port: "443", Deadline: time.Now().Add(time.Minute)}
	send("network-hold", hold)
	send("network-hold", hold)
	require.Len(t, notices, 1)
	assert.Equal(t, "network", notices[0].Kind)
	assert.True(t, r.IsNoticeActive(notices[0]))
	info, _ := r.GetWindow(w.ID)
	assert.Equal(t, 1, info.Pending)
	send("network-end", proxy.HoldEnd{ID: 1})
	require.Len(t, notices, 2)
	assert.Equal(t, "withdraw", notices[1].Kind)
	assert.False(t, r.IsNoticeActive(notices[0]), "queued notification cannot outlive resolution")
	send("agent-event", json.RawMessage(`{"type":"control_request","request_id":"tool1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`))
	send("agent-event", json.RawMessage(`{"type":"control_cancel_request","request_id":"tool1"}`))
	assert.Equal(t, "tool", notices[2].Kind)
	assert.Equal(t, "withdraw", notices[3].Kind)
	send("network-hold", proxy.LogEntry{ID: 2, Domain: "old.example", Deadline: time.Now().Add(time.Second)})
	r.ExpireNotices(time.Now().Add(2 * time.Second))
	assert.Equal(t, "withdraw", notices[len(notices)-1].Kind)
	info, _ = r.GetWindow(w.ID)
	assert.Zero(t, info.Pending)
	send("agent-status", "Running")
	send("agent-event", json.RawMessage(`{"type":"result","uuid":"result1","is_error":false,"terminal_reason":"completed"}`))
	send("agent-event", json.RawMessage(`{"type":"result","uuid":"result1","is_error":false}`))
	assert.Equal(t, "finished", notices[len(notices)-1].Kind)
	count := len(notices)
	send("agent-status", "Running")
	send("agent-event", json.RawMessage(`{"type":"result","uuid":"result2"}`))
	require.Len(t, notices, count+2, "second turn withdraws the previous notification and delivers its own")
	assert.Equal(t, "finished", notices[len(notices)-1].Kind)
	count = len(notices)
	r.receive(w.ID, Event{Connection: "stale", Kind: "network-hold", Data: hold})
	assert.Len(t, notices, count)
	require.NoError(t, r.Disconnect(w.ID))
	assert.Equal(t, "withdraw", notices[len(notices)-1].Kind)
}

func TestWindowHooksAndFailureCleanup(t *testing.T) {
	r := NewWorkspaces(true, nil, t.TempDir())
	defer r.Shutdown()
	opened, hidden := "", ""
	r.Hooks = DesktopHooks{Native: true, Open: func(id string) error { opened = id; return nil }, Hide: func(id string) error { hidden = id; return nil }}
	w, err := r.NewWindow()
	require.NoError(t, err)
	assert.True(t, w.Native)
	assert.Equal(t, w.ID, opened)
	require.NoError(t, r.HideWindow(w.ID))
	assert.Equal(t, w.ID, hidden)
	require.NoError(t, r.ShowWindow(w.ID))
	assert.Len(t, r.ListWindows(), 1, "show reuses window")
	require.Error(t, r.ShowWindow("missing"))
	r.Hooks.Open = func(string) error { return errors.New("window failed") }
	_, err = r.NewWindow()
	require.EqualError(t, err, "window failed")
	assert.Len(t, r.ListWindows(), 1)

}

func TestConcurrentWorkspaceShutdown(t *testing.T) {
	for range 30 {
		r := NewWorkspaces(true, nil, t.TempDir())
		w, err := r.NewWindow()
		require.NoError(t, err)
		token, err := r.Connect(w.ID, "demo")
		require.NoError(t, err)
		r.receive(w.ID, Event{Connection: token, Kind: "network-hold", Data: proxy.LogEntry{ID: 1, Deadline: time.Now().Add(time.Minute)}})
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _ = r.Disconnect(w.ID) }()
		go func() { defer wg.Done(); _, _ = r.Connect(w.ID, "demo") }()
		go func() { defer wg.Done(); r.Shutdown() }()
		wg.Wait()
		assert.Empty(t, r.ListWindows())
	}
}

func TestFocusedNotificationPreferenceIsPerWindow(t *testing.T) {
	r := NewWorkspaces(true, nil, t.TempDir())
	defer r.Shutdown()
	first, err := r.NewWindow()
	require.NoError(t, err)
	second, err := r.NewWindow()
	require.NoError(t, err)
	assert.False(t, first.NotifyWhenFocused)
	enabled, err := r.SetNotifyWhenFocused(first.ID, true)
	require.NoError(t, err)
	assert.True(t, enabled)
	_, err = r.Connect(first.ID, "demo")
	require.NoError(t, err)
	require.NoError(t, r.Disconnect(first.ID))
	first, err = r.GetWindow(first.ID)
	require.NoError(t, err)
	assert.True(t, first.NotifyWhenFocused, "reconnect keeps preference")
	second, err = r.GetWindow(second.ID)
	require.NoError(t, err)
	assert.False(t, second.NotifyWhenFocused, "other windows keep their preference")
	enabled, err = r.SetNotifyWhenFocused(first.ID, false)
	require.NoError(t, err)
	assert.False(t, enabled)
	_, err = r.SetNotifyWhenFocused("missing", true)
	require.Error(t, err)
}

func TestWorkspaceClipboardUsesNativeHooksAndRejectsUnknownWindow(t *testing.T) {
	r := NewWorkspaces(true, nil, t.TempDir())
	defer r.Shutdown()
	window, err := r.NewWindow()
	require.NoError(t, err)
	_, err = r.ReadClipboard(window.ID)
	require.ErrorContains(t, err, "unavailable")
	clipboard := ""
	r.Hooks.ReadClipboard = func() (string, error) { return clipboard, nil }
	r.Hooks.WriteClipboard = func(text string) error { clipboard = text; return nil }
	text := "printf 'héllo 🌍'\nsecond line\t"
	require.NoError(t, r.WriteClipboard(window.ID, text))
	got, err := r.ReadClipboard(window.ID)
	require.NoError(t, err)
	assert.Equal(t, text, got)
	require.Error(t, r.WriteClipboard("missing", "replace"))
	assert.Equal(t, text, clipboard)
	_, err = r.ReadClipboard("missing")
	require.Error(t, err)
	r.Hooks.ReadClipboard = func() (string, error) { return "", errors.New("clipboard unavailable") }
	_, err = r.ReadClipboard(window.ID)
	require.EqualError(t, err, "clipboard unavailable")
}
