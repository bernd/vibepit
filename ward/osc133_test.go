package ward

import (
	"bytes"
	"testing"
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

//go:fix inline
func intPtr(v int) *int { return new(v) }

func assertEvents(t *testing.T, data []byte, expected []parsedEvent) {
	t.Helper()
	got := parseEvents(data)
	if len(got) != len(expected) {
		t.Fatalf("expected %d events, got %d: %+v", len(expected), len(got), got)
	}
	for i := range expected {
		if got[i].event != expected[i].event {
			t.Errorf("event[%d]: expected %v, got %v", i, expected[i].event, got[i].event)
		}
		if (got[i].exitCode == nil) != (expected[i].exitCode == nil) {
			t.Errorf("event[%d] exitCode: expected %v, got %v", i, expected[i].exitCode, got[i].exitCode)
		} else if got[i].exitCode != nil && *got[i].exitCode != *expected[i].exitCode {
			t.Errorf("event[%d] exitCode: expected %d, got %d", i, *expected[i].exitCode, *got[i].exitCode)
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
	if p.Zone() != ZoneUnknown {
		t.Fatalf("expected ZoneUnknown, got %v", p.Zone())
	}
}

func TestFullZoneCycle(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;A\x07"), collect)
	if p.Zone() != ZonePrompt {
		t.Fatalf("expected ZonePrompt, got %v", p.Zone())
	}

	p.Push([]byte("\x1b]133;B\x07"), collect)
	if p.Zone() != ZoneInput {
		t.Fatalf("expected ZoneInput, got %v", p.Zone())
	}

	p.Push([]byte("\x1b]133;C\x07"), collect)
	if p.Zone() != ZoneOutput {
		t.Fatalf("expected ZoneOutput, got %v", p.Zone())
	}

	p.Push([]byte("\x1b]133;D;0\x07"), collect)
	if p.Zone() != ZoneUnknown {
		t.Fatalf("expected ZoneUnknown, got %v", p.Zone())
	}

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
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
	if len(events) != 0 {
		t.Fatal("expected no events after partial ESC")
	}
	p.Push([]byte("]133;A\x07"), collect)
	if len(events) != 1 || events[0].event != EventPromptStart {
		t.Fatalf("expected PromptStart, got %+v", events)
	}
}

func TestSplitMidParam(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]13"), collect)
	if len(events) != 0 {
		t.Fatal("expected no events after partial param")
	}
	p.Push([]byte("3;D;42\x07"), collect)
	if len(events) != 1 || events[0].event != EventCommandFinished || *events[0].exitCode != 42 {
		t.Fatalf("expected CommandFinished with exit code 42, got %+v", events)
	}
}

func TestSplitBeforeTerminator(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;B"), collect)
	if len(events) != 0 {
		t.Fatal("expected no events before terminator")
	}
	p.Push([]byte("\x07"), collect)
	if len(events) != 1 || events[0].event != EventCommandStart {
		t.Fatalf("expected CommandStart, got %+v", events)
	}
}

func TestSplitEscBackslashTerminator(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	p.Push([]byte("\x1b]133;C\x1b"), collect)
	if len(events) != 0 {
		t.Fatal("expected no events before ST completion")
	}
	p.Push([]byte("\\"), collect)
	if len(events) != 1 || events[0].event != EventCommandExecuted {
		t.Fatalf("expected CommandExecuted, got %+v", events)
	}
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
	data := []byte("\x1b]7;file:///home/user\x07")
	events := parseEvents(data)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

// --- Unknown command letter ---

func TestUnknownCommandIgnored(t *testing.T) {
	data := []byte("\x1b]133;Z\x07")
	events := parseEvents(data)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
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
	if len(events) != 0 {
		t.Fatal("expected no events for lone ESC")
	}
	p.Push([]byte("x\x1b]133;A\x07"), collect)
	if len(events) != 1 || events[0].event != EventPromptStart {
		t.Fatalf("expected PromptStart, got %+v", events)
	}
}

func TestTruncated133Prefix(t *testing.T) {
	data := []byte("\x1b]13\x07")
	events := parseEvents(data)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

func TestEmptyOsc(t *testing.T) {
	data := []byte("\x1b]\x07")
	events := parseEvents(data)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

// --- Buffer overflow ---

func TestVeryLongOscDoesNotPanic(t *testing.T) {
	var data []byte
	data = append(data, '\x1b', ']')
	data = append(data, bytes.Repeat([]byte{'x'}, 1000)...)
	data = append(data, bel)
	events := parseEvents(data)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

// --- Empty input ---

func TestEmptyInput(t *testing.T) {
	events := parseEvents([]byte{})
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

func TestOnlyNormalText(t *testing.T) {
	events := parseEvents([]byte("just some regular terminal output\r\n"))
	if len(events) != 0 {
		t.Fatalf("expected no events, got %+v", events)
	}
}

// --- Repeated prompts (empty command) ---

func TestRepeatedPromptCycle(t *testing.T) {
	p := NewOSC133Parser()
	var events []parsedEvent
	collect := func(e Event, code *int) { events = append(events, parsedEvent{e, code}) }

	data := []byte("\x1b]133;A\x07$ \x1b]133;B\x07\x1b]133;D\x07\x1b]133;A\x07$ \x1b]133;B\x07")
	p.Push(data, collect)

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d: %+v", len(events), events)
	}
	if p.Zone() != ZoneInput {
		t.Fatalf("expected ZoneInput, got %v", p.Zone())
	}
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
	if len(events) != 1 || events[0].event != EventCommandFinished || *events[0].exitCode != 99 {
		t.Fatalf("expected CommandFinished with exit code 99, got %+v", events)
	}
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
