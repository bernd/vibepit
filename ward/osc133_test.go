package ward

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test helpers ---

type parsedEvent struct {
	event    Event
	exitCode *int
}

func parseEvents(data []byte) []parsedEvent {
	p := NewOSC133Parser()
	var events []parsedEvent
	p.Push(data, func(e Event, code *int) {
		events = append(events, parsedEvent{e, code})
	})
	return events
}

func assertEvents(t *testing.T, data []byte, expected []parsedEvent) {
	t.Helper()
	got := parseEvents(data)
	require.Len(t, got, len(expected))
	for i := range expected {
		assert.Equal(t, expected[i].event, got[i].event, "event[%d]", i)
		if expected[i].exitCode == nil {
			assert.Nil(t, got[i].exitCode, "event[%d] exitCode", i)
		} else {
			require.NotNil(t, got[i].exitCode, "event[%d] exitCode", i)
			assert.Equal(t, *expected[i].exitCode, *got[i].exitCode, "event[%d] exitCode value", i)
		}
	}
}

// --- Basic event detection ---

func TestDetectPromptStartBel(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;A\x07"), []parsedEvent{{EventPromptStart, nil}})
}

func TestDetectPromptStartST(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;A\x1b\\"), []parsedEvent{{EventPromptStart, nil}})
}

func TestDetectCommandStartBel(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;B\x07"), []parsedEvent{{EventCommandStart, nil}})
}

func TestDetectCommandStartST(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;B\x1b\\"), []parsedEvent{{EventCommandStart, nil}})
}

func TestDetectCommandExecutedBel(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;C\x07"), []parsedEvent{{EventCommandExecuted, nil}})
}

func TestDetectCommandExecutedST(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;C\x1b\\"), []parsedEvent{{EventCommandExecuted, nil}})
}

func TestDetectCommandFinishedNoExitCode(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D\x07"), []parsedEvent{{EventCommandFinished, nil}})
}

func TestDetectCommandFinishedExitZero(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D;0\x07"), []parsedEvent{{EventCommandFinished, new(0)}})
}

func TestDetectCommandFinishedExitNonzero(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D;127\x07"), []parsedEvent{{EventCommandFinished, new(127)}})
}

func TestDetectCommandFinishedNegativeExitCode(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D;-1\x07"), []parsedEvent{{EventCommandFinished, new(-1)}})
}

func TestDetectCommandFinishedExitCodeST(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D;42\x1b\\"), []parsedEvent{{EventCommandFinished, new(42)}})
}

func TestInvalidExitCodeYieldsNil(t *testing.T) {
	assertEvents(t, []byte("\x1b]133;D;abc\x07"), []parsedEvent{{EventCommandFinished, nil}})
}

// --- Zone tracking ---

func TestZoneStartsUnknown(t *testing.T) {
	p := NewOSC133Parser()
	require.Equal(t, ZoneUnknown, p.Zone())
}

func TestFullZoneCycle(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;A\x07"), collect)
	require.Equal(t, ZonePrompt, p.Zone())

	p.Push([]byte("\x1b]133;B\x07"), collect)
	require.Equal(t, ZoneInput, p.Zone())

	p.Push([]byte("\x1b]133;C\x07"), collect)
	require.Equal(t, ZoneOutput, p.Zone())

	p.Push([]byte("\x1b]133;D;0\x07"), collect)
	require.Equal(t, ZoneUnknown, p.Zone())

	require.Len(t, events, 4)
}

// --- Multiple events in one push ---

func TestMultipleEventsSinglePush(t *testing.T) {
	data := []byte("\x1b]133;A\x07$ \x1b]133;B\x07ls\n\x1b]133;C\x07file.txt\n\x1b]133;D;0\x07")
	assertEvents(t, data, []parsedEvent{
		{EventPromptStart, nil},
		{EventCommandStart, nil},
		{EventCommandExecuted, nil},
		{EventCommandFinished, new(0)},
	})
}

// --- Split across push boundaries ---

func TestSplitEscAndBracket(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b"), collect)
	require.Empty(t, events, "no events after partial ESC")

	p.Push([]byte("]133;A\x07"), collect)
	require.Len(t, events, 1)
	assert.Equal(t, EventPromptStart, events[0].event)
}

func TestSplitMidParam(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]13"), collect)
	require.Empty(t, events, "no events after partial param")

	p.Push([]byte("3;D;42\x07"), collect)
	require.Len(t, events, 1)
	assert.Equal(t, EventCommandFinished, events[0].event)
	require.NotNil(t, events[0].exitCode)
	assert.Equal(t, 42, *events[0].exitCode)
}

func TestSplitBeforeTerminator(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;B"), collect)
	require.Empty(t, events, "no events before terminator")

	p.Push([]byte("\x07"), collect)
	require.Len(t, events, 1)
	assert.Equal(t, EventCommandStart, events[0].event)
}

func TestSplitEscBackslashTerminator(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;C\x1b"), collect)
	require.Empty(t, events, "no events before ST completion")

	p.Push([]byte("\\"), collect)
	require.Len(t, events, 1)
	assert.Equal(t, EventCommandExecuted, events[0].event)
}

// --- Interleaved normal text ---

func TestNormalTextBeforeAndAfter(t *testing.T) {
	data := []byte("hello world\x1b]133;A\x07prompt text\x1b]133;B\x07command")
	assertEvents(t, data, []parsedEvent{
		{EventPromptStart, nil},
		{EventCommandStart, nil},
	})
}

// --- Non-133 OSC sequences (should be ignored) ---

func TestNon133OscIgnored(t *testing.T) {
	data := []byte("\x1b]0;window title\x07\x1b]133;A\x07")
	assertEvents(t, data, []parsedEvent{{EventPromptStart, nil}})
}

func TestOsc7Ignored(t *testing.T) {
	events := parseEvents([]byte("\x1b]7;file:///home/user\x07"))
	require.Empty(t, events)
}

// --- Unknown command letter ---

func TestUnknownCommandIgnored(t *testing.T) {
	events := parseEvents([]byte("\x1b]133;Z\x07"))
	require.Empty(t, events)
}

// --- Malformed sequences ---

func TestEscFollowedByNonBracket(t *testing.T) {
	data := []byte("\x1b[31m\x1b]133;A\x07")
	assertEvents(t, data, []parsedEvent{{EventPromptStart, nil}})
}

func TestLoneEscAtEndOfChunk(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b"), collect)
	require.Empty(t, events, "no events for lone ESC")

	p.Push([]byte("x\x1b]133;A\x07"), collect)
	require.Len(t, events, 1)
	assert.Equal(t, EventPromptStart, events[0].event)
}

func TestTruncated133Prefix(t *testing.T) {
	events := parseEvents([]byte("\x1b]13\x07"))
	require.Empty(t, events)
}

func TestEmptyOsc(t *testing.T) {
	events := parseEvents([]byte("\x1b]\x07"))
	require.Empty(t, events)
}

// --- Buffer overflow ---

func TestVeryLongOscDoesNotPanic(t *testing.T) {
	var data []byte
	data = append(data, '\x1b', ']')
	data = append(data, bytes.Repeat([]byte{'x'}, 1000)...)
	data = append(data, bel)
	events := parseEvents(data)
	require.Empty(t, events)
}

// --- Empty input ---

func TestEmptyInput(t *testing.T) {
	events := parseEvents([]byte{})
	require.Empty(t, events)
}

func TestOnlyNormalText(t *testing.T) {
	events := parseEvents([]byte("just some regular terminal output\r\n"))
	require.Empty(t, events)
}

// --- Repeated prompts (empty command) ---

func TestRepeatedPromptCycle(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	data := []byte("\x1b]133;A\x07$ \x1b]133;B\x07\x1b]133;D\x07\x1b]133;A\x07$ \x1b]133;B\x07")
	p.Push(data, collect)

	require.Len(t, events, 5)
	require.Equal(t, ZoneInput, p.Zone())
}

// --- Byte-at-a-time feeding ---

func TestByteAtATime(t *testing.T) {
	data := []byte("\x1b]133;D;99\x07")
	p := NewOSC133Parser()
	var events []parsedEvent
	for _, b := range data {
		p.Push([]byte{b}, func(e Event, code *int) {
			events = append(events, parsedEvent{e, code})
		})
	}
	require.Len(t, events, 1)
	assert.Equal(t, EventCommandFinished, events[0].event)
	require.NotNil(t, events[0].exitCode)
	assert.Equal(t, 99, *events[0].exitCode)
}

// --- Mixed terminators ---

func TestMixedBelAndSTTerminators(t *testing.T) {
	data := []byte("\x1b]133;A\x07\x1b]133;B\x1b\\\x1b]133;C\x07\x1b]133;D;1\x1b\\")
	assertEvents(t, data, []parsedEvent{
		{EventPromptStart, nil},
		{EventCommandStart, nil},
		{EventCommandExecuted, nil},
		{EventCommandFinished, new(1)},
	})
}

// --- D with empty exit code field ---

func TestDWithSemicolonButEmptyCode(t *testing.T) {
	data := []byte("\x1b]133;D;\x07")
	assertEvents(t, data, []parsedEvent{{EventCommandFinished, nil}})
}

// --- Consecutive OSC sequences ---

func TestBackToBackOscNoGap(t *testing.T) {
	data := []byte("\x1b]133;A\x07\x1b]133;B\x07")
	assertEvents(t, data, []parsedEvent{
		{EventPromptStart, nil},
		{EventCommandStart, nil},
	})
}

// --- CSI sequences interleaved ---

func TestCSISequencesIgnored(t *testing.T) {
	data := []byte("\x1b[32m\x1b]133;A\x07\x1b[0m$ \x1b]133;B\x07")
	assertEvents(t, data, []parsedEvent{
		{EventPromptStart, nil},
		{EventCommandStart, nil},
	})
}

// --- Large exit codes ---

func TestLargeExitCode(t *testing.T) {
	data := []byte("\x1b]133;D;2147483647\x07")
	assertEvents(t, data, []parsedEvent{{EventCommandFinished, new(2147483647)}})
}

func TestOverflowExitCodeYieldsNil(t *testing.T) {
	data := []byte("\x1b]133;D;9999999999999\x07")
	assertEvents(t, data, []parsedEvent{{EventCommandFinished, nil}})
}
