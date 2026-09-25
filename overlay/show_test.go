package overlay

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyModel shows its text and width full-screen and quits on the first
// key.
type keyModel struct {
	text       string
	panicOnKey bool
	width      int
}

func (m keyModel) Init() tea.Cmd { return nil }

func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyPressMsg:
		if m.panicOnKey {
			panic("prompt failed")
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m keyModel) View() tea.View {
	v := tea.NewView(fmt.Sprintf("%s W=%d", m.text, m.width))
	v.AltScreen = true
	return v
}

var promptModel = keyModel{text: "PROMPT"}

// prompt shows promptModel, waits until it is drawn, and answers it with
// a key.
func (h *harness) prompt(during func()) {
	h.t.Helper()
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	if during != nil {
		during()
	}
	h.keys("x")
	require.NoError(h.t, h.result(done))
	assert.NotContains(h.t, screenText(h.real), "PROMPT", "the prompt is gone")
}

func TestShowRestoresThePrimaryScreenByReplay(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ls\r\nfile\r\n\x1b[2;5r\x1b[4h\x1b(0\x1b[31m\x1b[6;1H" + strings.Repeat("x", 20) + "\x1b7")
	h.prompt(func() { h.app("q\x1b[0m\r\nmore") })
	assert.False(t, h.snapshotted(), "raw replay")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowOnTheAlternateScreenRestoresFromTheShadow(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[2;3Hvim")
	h.prompt(func() { h.app("\x1b[3;1H~edited") })
	assert.True(t, h.snapshotted(), "the prompt drew over the app's screen")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowChangesOnlySetD(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("\x1b[?2004h\x1b[?1004h\x1b[?1000h\x1b[>1u\x1b]2;app\x07\x1b]7;file://h/p\x07\x1b[3 qtext")
	before := stateOf(t, h.real)
	setD := map[modeKey]bool{
		modeIRM: true, {20, true}: true, {5, false}: true, {6, false}: true,
		{7, false}: true, {25, false}: true, modeSync: true, {1047, false}: true,
	}
	h.prompt(func() {
		during := stateOf(t, h.real)
		for i, m := range during.Modes {
			if !setD[modeKey{m.Mode, m.ANSI}] {
				assert.Equal(t, before.Modes[i].Value, m.Value, "mode %d (ANSI %v) changed", m.Mode, m.ANSI)
			}
		}
		assert.Equal(t, before.Title, during.Title)
		assert.Equal(t, before.Pwd, during.Pwd)
		assert.Equal(t, before.Cursor, during.Cursor)
	})
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowAnswersQueriesWhilePromptingOnce(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.prompt(func() {
		h.app("\x1b[6n")
		h.waitContainer("\x1b[1;1R")
	})
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "answered once")
	assert.True(t, h.snapshotted(), "a replay would make the real terminal answer again")
}

func TestShowKeepsTheSavedCursor(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("ab\x1b7cd")
	h.prompt(nil)
	h.app("\x1b8Z")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowKeepsTheKittyStack(t *testing.T) {
	for name, before := range map[string]string{
		"primary screen":   "\x1b[>1u\x1b[>3u",
		"alternate screen": "\x1b[?1049h\x1b[>1u\x1b[>3u",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 20, 6)
			h.app(before)
			h.prompt(nil)
			for range 2 {
				h.app("\x1b[<u")
				want, err := h.term.shadow.KittyKeyboardFlags()
				require.NoError(t, err)
				got, err := h.real.KittyKeyboardFlags()
				require.NoError(t, err)
				assert.Equal(t, want, got)
			}
		})
	}
}

func TestShowKeysGoToThePrompt(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.keys("before")
	h.waitContainer("before")
	h.prompt(nil)
	h.keys("after")
	h.waitContainer("beforeafter")
}

func TestShowBarrierTimeout(t *testing.T) {
	h := newHarness(t, 20, 6, silentTerminal(), withTiming(func(tm *timing) { tm.barrierWait = 50 * time.Millisecond }))
	h.app("$ ")
	for range barrierTimeoutsBeforeDegraded {
		assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrBarrierTimeout)
	}
	assert.NotContains(t, h.stdout.String(), "\x1b[?1047h", "nothing was drawn")
	h.locked(func(tm *Terminal) { assert.True(t, tm.barrierUnsupported) })
	h.prompt(nil)
	assert.Equal(t, barrierTimeoutsBeforeDegraded, strings.Count(h.stdout.String(), barrierQuery),
		"no barrier query once degraded")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowRestoresWhenTheProgramPanics(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), keyModel{text: "PROMPT", panicOnKey: true})
	h.waitScreen("PROMPT")
	h.keys("x")
	assert.ErrorIs(t, h.result(done), tea.ErrProgramPanic)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
	h.keys("k")
	h.waitContainer("k")
}

func TestShowCancelled(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.show(ctx, promptModel)
	h.waitScreen("PROMPT")
	cancel()
	assert.ErrorIs(t, h.result(done), context.Canceled)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowEndsWhenTheContainerExits(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	h.out.close()
	assert.ErrorIs(t, h.result(done), ErrClosed)
	<-h.done
	assert.NoError(t, h.err)
	assert.NotContains(t, screenText(h.real), "PROMPT")
}

func TestShowEndsAtStdinEOF(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	require.NoError(t, h.stdin.Close())
	assert.ErrorIs(t, h.result(done), ErrClosed)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowAfterRunReturned(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.out.close()
	<-h.done
	written := h.stdout.String()
	assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrClosed)
	assert.Equal(t, written, h.stdout.String(), "nothing written")
}

func TestShowLeaveIsAtomic(t *testing.T) {
	h := newHarness(t, 20, 6)
	stop, flooded := make(chan struct{}), make(chan struct{})
	h.prompt(func() {
		go func() {
			defer close(flooded)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if !h.out.push(fmt.Sprintf("\x1b[3%dm%d\x1b[0m\r\n", i%8, i)) {
					return
				}
			}
		}()
	})
	close(stop)
	<-flooded
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowWaitsForAnEarlierShow(t *testing.T) {
	h := newHarness(t, 20, 6)
	first := h.show(context.Background(), keyModel{text: "FIRST"})
	h.waitScreen("FIRST")
	second := h.show(context.Background(), keyModel{text: "SECOND"})
	time.Sleep(50 * time.Millisecond)
	assert.NotContains(t, screenText(h.real), "SECOND")
	h.keys("x")
	require.NoError(t, h.result(first))
	h.waitScreen("SECOND")
	h.keys("x")
	require.NoError(t, h.result(second))
}

func TestShowWithoutShadowIsUnavailable(t *testing.T) {
	h := newHarness(t, 20, 6, withoutShadow())
	assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrUnavailable)
}

func TestResizeWhilePrompting(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	h.prompt(func() {
		h.waitScreen("PROMPT W=20")
		h.resize(30, 8)
		h.waitScreen("PROMPT W=30")
		h.app("output for the new width")
	})
	assert.True(t, h.snapshotted(), "the log was written for the old size")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowFloodPastLogCap(t *testing.T) {
	h := newHarness(t, 20, 6, withTiming(func(tm *timing) { tm.logMax = 1024 }))
	h.prompt(func() {
		start := time.Now()
		for i := range 200 {
			h.app(fmt.Sprintf("y %d\r\n", i))
		}
		assert.Less(t, time.Since(start), 5*time.Second, "output never waits for the prompt")
		h.locked(func(tm *Terminal) { assert.True(t, tm.log.overflow) })
	})
	assert.True(t, h.snapshotted())
	assertSameTerminal(t, h.term.shadow, h.real)
}
