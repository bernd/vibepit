package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
)

// recordModel records the messages it receives.
type recordModel struct{ got *[]string }

func (m recordModel) Init() tea.Cmd { return nil }
func (m recordModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		*m.got = append(*m.got, msg.String())
	case tea.PasteMsg:
		*m.got = append(*m.got, "paste:"+msg.Content)
	case TickMsg:
		*m.got = append(*m.got, "tick")
	}
	return m, nil
}
func (m recordModel) View() tea.View { return tea.NewView("") }

// gateEvent is a message arriving at an offset from the gate's start.
// A nil msg lets the pending settle tick fire.
type gateEvent struct {
	at  time.Duration
	msg tea.Msg
}

func TestKeyGate(t *testing.T) {
	const quiet, settle = 400 * time.Millisecond, 250 * time.Millisecond
	key := func(r rune) tea.Msg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

	tests := []struct {
		name   string
		events []gateEvent
		want   []string
	}{
		{
			name:   "a single key after a pause, once it settled",
			events: []gateEvent{{450 * time.Millisecond, key('n')}, {700 * time.Millisecond, nil}},
			want:   []string{"n"},
		},
		{
			name:   "a key not yet settled is not delivered",
			events: []gateEvent{{450 * time.Millisecond, key('n')}},
			want:   nil,
		},
		{
			name: "continuous typing never answers",
			events: []gateEvent{
				{120 * time.Millisecond, key('h')}, {240 * time.Millisecond, key('e')},
				{360 * time.Millisecond, key('l')}, {480 * time.Millisecond, key('a')},
				{600 * time.Millisecond, key('A')}, {720 * time.Millisecond, key('n')},
				{1000 * time.Millisecond, nil},
			},
			want: nil,
		},
		{
			name: "a key after a pause but followed by typing is typing",
			events: []gateEvent{
				{500 * time.Millisecond, key('a')}, {550 * time.Millisecond, key('l')},
				{1000 * time.Millisecond, nil},
			},
			want: nil,
		},
		{
			name: "once a deliberate key got through, the gate stays open",
			events: []gateEvent{
				{500 * time.Millisecond, key('A')}, {760 * time.Millisecond, nil},
				{800 * time.Millisecond, key('y')},
			},
			want: []string{"A", "y"},
		},
		{
			name: "pastes are held back and restart the quiet period",
			events: []gateEvent{
				{300 * time.Millisecond, tea.PasteMsg{Content: "A"}},
				{600 * time.Millisecond, key('a')}, {900 * time.Millisecond, nil},
			},
			want: nil,
		},
		{
			name: "esc and ctrl+c are gated like any key",
			events: []gateEvent{
				{10 * time.Millisecond, key('x')},
				{20 * time.Millisecond, tea.KeyPressMsg{Code: tea.KeyEscape}},
				{30 * time.Millisecond, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}},
				{500 * time.Millisecond, tea.KeyPressMsg{Code: tea.KeyEscape}}, {800 * time.Millisecond, nil},
			},
			want: []string{"esc"},
		},
		{
			name: "keys that are no answer never open the gate",
			events: []gateEvent{
				{500 * time.Millisecond, tea.KeyPressMsg{Code: tea.KeyEnter}}, {800 * time.Millisecond, nil},
				{900 * time.Millisecond, key('a')}, {950 * time.Millisecond, key('n')}, {1000 * time.Millisecond, key('d')},
				{1300 * time.Millisecond, nil},
			},
			want: nil,
		},
		{
			name: "modifier keys on their own do not count",
			events: []gateEvent{
				{450 * time.Millisecond, tea.KeyPressMsg{Code: tea.KeyLeftShift}},
				{500 * time.Millisecond, tea.KeyPressMsg{Code: 'a', ShiftedCode: 'A', Mod: tea.ModShift, Text: "A"}},
				{550 * time.Millisecond, tea.KeyPressMsg{Code: tea.KeyRightCtrl}},
				{800 * time.Millisecond, nil},
			},
			want: []string{"A"},
		},
		{
			name: "a held key repeats before it settles",
			events: []gateEvent{
				{450 * time.Millisecond, key('a')},
				{700 * time.Millisecond, tea.KeyPressMsg{Code: 'a', Text: "a", IsRepeat: true}},
				{1000 * time.Millisecond, nil},
			},
			want: nil,
		},
		{
			name: "where releases are reported, a key still down when it settles is held",
			events: []gateEvent{
				{100 * time.Millisecond, tea.KeyReleaseMsg{Code: 'x'}},
				{500 * time.Millisecond, key('a')}, {1000 * time.Millisecond, nil},
				{1100 * time.Millisecond, tea.KeyReleaseMsg{Code: 'a'}},
			},
			want: nil,
		},
		{
			name: "where releases are reported, a tapped key answers",
			events: []gateEvent{
				{450 * time.Millisecond, key('a')},
				{500 * time.Millisecond, tea.KeyReleaseMsg{Code: 'a'}},
				{1000 * time.Millisecond, nil},
			},
			want: []string{"a"},
		},
		{
			name: "other messages pass and do not restart the quiet period",
			events: []gateEvent{
				{100 * time.Millisecond, TickMsg{}},
				{390 * time.Millisecond, tea.MouseMotionMsg{}},
				{400 * time.Millisecond, key('n')}, {700 * time.Millisecond, nil},
			},
			want: []string{"tick", "n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			start := time.Unix(1000, 0)
			now := start
			answers := func(k tea.KeyPressMsg) bool {
				return k.Text != "" && strings.ContainsAny(k.Text, "aAnxyz") || k.String() == "esc"
			}
			g := NewKeyGate(recordModel{got: &got}, answers, quiet, settle)
			g.now = func() time.Time { return now }
			g.Init()
			var tick tea.Cmd
			for _, e := range tt.events {
				now = start.Add(e.at)
				msg := e.msg
				if msg == nil {
					if tick == nil {
						continue
					}
					// Stand in for the tick firing: its message, now.
					msg = settledMsg{gate: g, seq: g.seq}
					tick = nil
				}
				if _, cmd := g.Update(msg); cmd != nil {
					tick = cmd
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKeyGate_AsksForReleases(t *testing.T) {
	var got []string
	g := NewKeyGate(recordModel{got: &got}, func(tea.KeyPressMsg) bool { return true }, time.Second, time.Second)
	assert.True(t, g.View().KeyboardEnhancements.ReportEventTypes)
}
