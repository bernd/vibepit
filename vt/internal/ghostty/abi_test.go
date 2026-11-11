package ghostty

import (
	"encoding/json"
	"math/bits"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestABIMatchesTypeJSON compares every hard-coded layout and enum value
// with the module's own description. A ghostty bump that changes one fails
// here, not in production.
func TestABIMatchesTypeJSON(t *testing.T) {
	in, err := NewInstance()
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	raw, err := in.TypeJSON()
	require.NoError(t, err)

	var doc struct {
		ABI struct {
			Target      string `json:"target"`
			PointerSize int    `json:"pointer_size"`
			UsizeSize   int    `json:"usize_size"`
			Endian      string `json:"endian"`
		} `json:"abi"`
		Types map[string]struct {
			Size   uint32 `json:"size"`
			Fields map[string]struct {
				Offset uint32 `json:"offset"`
			} `json:"fields"`
			Values map[string]int64 `json:"values"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	assert.Equal(t, "wasm32", doc.ABI.Target)
	assert.Equal(t, 4, doc.ABI.PointerSize)
	assert.Equal(t, 4, doc.ABI.UsizeSize)
	assert.Equal(t, "little", doc.ABI.Endian)

	structs := []struct {
		name   string
		size   uint32
		fields map[string]uint32
	}{
		{"GhosttyString", sizeofString, map[string]uint32{"ptr": offStringPtr, "len": offStringLen}},
		{"GhosttyTerminalModeConfig", sizeofModeConfig, map[string]uint32{"mode": offModeConfigMode, "value": offModeConfigValue}},
		{"GhosttyPoint", sizeofPoint, map[string]uint32{"tag": offPointTag, "value": offPointValue}},
		{"GhosttyPointCoordinate", sizeofCoordinate, map[string]uint32{"x": offCoordinateX, "y": offCoordinateY}},
		{"GhosttyGridRef", sizeofGridRef, map[string]uint32{
			"node": offGridRefNode, "x": offGridRefX, "y": offGridRefY,
		}},
		{"GhosttySelection", sizeofSelection, map[string]uint32{
			"start": offSelectionStart, "end": offSelectionEnd, "rectangle": offSelectionRectangle,
		}},
		{"GhosttyTerminalWriteStop", sizeofWriteStop, map[string]uint32{
			"size": offWriteStopSize, "consumed": offWriteStopConsumed, "reached": offWriteStopReached,
			"kind": offWriteStopKind, "introducer": offWriteStopIntroducer, "end": offWriteStopEnd, "byte": offWriteStopByte,
		}},
	}
	for _, s := range structs {
		t.Run(s.name, func(t *testing.T) {
			typ, ok := doc.Types[s.name]
			require.True(t, ok, "type missing from ghostty_type_json")
			assert.Equal(t, s.size, typ.Size, "size")
			assert.Len(t, typ.Fields, len(s.fields), "field count")
			for field, off := range s.fields {
				got, ok := typ.Fields[field]
				if assert.True(t, ok, "field %s missing", field) {
					assert.Equal(t, off, got.Offset, "offset of %s", field)
				}
			}
		})
	}

	enums := []struct {
		typ, name string
		want      int64
	}{
		{"GhosttyResult", "SUCCESS", int64(Success)},
		{"GhosttyResult", "OUT_OF_MEMORY", int64(OutOfMemory)},
		{"GhosttyResult", "INVALID_VALUE", int64(InvalidValue)},
		{"GhosttyResult", "OUT_OF_SPACE", int64(OutOfSpace)},
		{"GhosttyResult", "NO_VALUE", int64(NoValue)},
		{"GhosttyResult", "IO_ERROR", int64(IOError)},
		{"GhosttyResult", "LIMIT_EXCEEDED", int64(LimitExceeded)},
		{"GhosttyResult", "REJECTED", int64(Rejected)},
		{"GhosttyResult", "UNSUPPORTED", int64(Unsupported)},
		{"GhosttyTerminalOption", "WRITE_PTY", int64(OptWritePty)},
		{"GhosttyTerminalOption", "SCROLLBACK_MAX_BYTES", int64(OptScrollbackMaxBytes)},
		{"GhosttyTerminalOption", "SCROLLBACK_MAX_LINES", int64(OptScrollbackMaxLines)},
		{"GhosttyTerminalOption", "CONTINUATION_MAX_BYTES", int64(OptContinuationMaxBytes)},
		{"GhosttyTerminalOption", "MODE", int64(OptMode)},
		{"GhosttyTerminalData", "COLS", int64(DataCols)},
		{"GhosttyTerminalData", "ROWS", int64(DataRows)},
		{"GhosttyTerminalData", "CURSOR_X", int64(DataCursorX)},
		{"GhosttyTerminalData", "CURSOR_Y", int64(DataCursorY)},
		{"GhosttyTerminalData", "CURSOR_PENDING_WRAP", int64(DataCursorPendingWrap)},
		{"GhosttyTerminalData", "ACTIVE_SCREEN", int64(DataActiveScreen)},
		{"GhosttyTerminalData", "CURSOR_VISIBLE", int64(DataCursorVisible)},
		{"GhosttyTerminalData", "KITTY_KEYBOARD_FLAGS", int64(DataKittyKeyboardFlags)},
		{"GhosttyTerminalData", "MOUSE_TRACKING", int64(DataMouseTracking)},
		{"GhosttyTerminalData", "TITLE", int64(DataTitle)},
		{"GhosttyTerminalData", "PWD", int64(DataPwd)},
		{"GhosttyTerminalData", "TOTAL_ROWS", int64(DataTotalRows)},
		{"GhosttyTerminalData", "SCROLLBACK_ROWS", int64(DataScrollbackRows)},
		{"GhosttyTerminalData", "SCROLLBACK_MAX_BYTES", int64(DataScrollbackMaxBytes)},
		{"GhosttyTerminalData", "SCROLLBACK_MAX_LINES", int64(DataScrollbackMaxLines)},
		{"GhosttyTerminalData", "CONTINUATION_MAX_BYTES", int64(DataContinuationMaxBytes)},
		{"GhosttyTerminalData", "MODE", int64(DataMode)},
		{"GhosttyTerminalData", "VT_GROUND", int64(DataVTGround)},
		{"GhosttyTerminalScreen", "PRIMARY", int64(ScreenPrimary)},
		{"GhosttyTerminalScreen", "ALTERNATE", int64(ScreenAlternate)},
		{"GhosttyFormatterFormat", "PLAIN", int64(FormatPlain)},
		{"GhosttyFormatterFormat", "VT", int64(FormatVT)},
		{"GhosttyFormatterFormat", "HTML", int64(FormatHTML)},
		{"GhosttyFormatterOption", "EMIT", int64(FmtOptEmit)},
		{"GhosttyFormatterOption", "UNWRAP", int64(FmtOptUnwrap)},
		{"GhosttyFormatterOption", "TRIM", int64(FmtOptTrim)},
		{"GhosttyFormatterOption", "SELECTION", int64(FmtOptSelection)},
		{"GhosttyFormatterOption", "EXTRAS", int64(FmtOptExtras)},
		{"GhosttyFormatterOption", "CONTENT_NONE", int64(FmtOptContentNone)},
		{"GhosttyTerminalBoundary", "GROUND", int64(BoundaryGround)},
		{"GhosttyPointTag", "ACTIVE", int64(PointActive)},
		{"GhosttyPointTag", "VIEWPORT", int64(PointViewport)},
		{"GhosttyPointTag", "SCREEN", int64(PointScreen)},
		{"GhosttyPointTag", "HISTORY", int64(PointHistory)},
		{"GhosttyRenderStateData", "CURSOR_VISUAL_STYLE", int64(RenderDataCursorVisualStyle)},
		{"GhosttyRenderStateData", "CURSOR_BLINKING", int64(RenderDataCursorBlinking)},
		{"GhosttyCursorStyle", "BAR", int64(CursorVisualBar)},
		{"GhosttyCursorStyle", "BLOCK", int64(CursorVisualBlock)},
		{"GhosttyCursorStyle", "UNDERLINE", int64(CursorVisualUnderline)},
		{"GhosttyCursorStyle", "BLOCK_HOLLOW", int64(CursorVisualBlockHollow)},
	}
	for _, e := range enums {
		v, ok := doc.Types[e.typ].Values[e.name]
		if assert.True(t, ok, "%s.%s missing", e.typ, e.name) {
			assert.Equal(t, e.want, v, "%s.%s", e.typ, e.name)
		}
	}
}

// TestFormatterExtrasComplete fails when upstream adds an extra: a bit past
// the known ones becomes valid.
func TestFormatterExtrasComplete(t *testing.T) {
	in := newTerm(t, 10, 3)
	require.NoError(t, in.invoke("ghostty_formatter_terminal_new", func() int32 {
		return in.mod.Xghostty_formatter_terminal_new(0, int32(in.slot), int32(in.term))
	}))
	f, err := in.takeOpaque()
	require.NoError(t, err)
	defer in.mod.Xghostty_formatter_free(int32(f))

	all := allExtras.Bits()
	require.NoError(t, in.formatterSet(f, FmtOptExtras, all))
	for bit := bits.Len32(all); bit < 32; bit++ {
		err := in.formatterSet(f, FmtOptExtras, 1<<bit)
		assert.ErrorIs(t, err, InvalidValue, "upstream added a formatter extra at bit %d", bit)
	}
}

var allExtras = TerminalExtra{
	Palette: true, Modes: true, ScrollingRegion: true, Tabstops: true, Pwd: true, Keyboard: true,
	Screen: ScreenExtra{Cursor: true, Style: true, Hyperlink: true, Protection: true, KittyKeyboard: true, Charsets: true},
}

// TestFormatterExtraBits pins each GHOSTTY_FORMATTER_EXTRA_* bit by what it
// emits alone: its own state and nobody else's.
func TestFormatterExtraBits(t *testing.T) {
	in := newTerm(t, 20, 6)
	feed(t, in, "\x1b]4;1;rgb:12/34/56\x1b\\"+ // palette
		"\x1b[?1h"+ // DECCKM
		"\x1b[2;5r"+ // scrolling region
		"\x1b[3g"+ // no tab stops
		"\x1b]7;file://h/tmp\x07"+ // pwd
		"\x1b[>4;2m"+ // modifyOtherKeys
		"\x1b[>1u"+ // kitty keyboard
		"\x1b(0"+ // G0 line drawing
		"\x1b[1;31m"+ // pen style
		"\x1b]8;;http://x\x1b\\"+ // pen hyperlink
		"\x1b[1\"q"+ // DECSCA
		"\x1b[3;4H") // cursor

	extras := []struct {
		name   string
		set    func(*TerminalExtra)
		marker string
	}{
		{"palette", func(x *TerminalExtra) { x.Palette = true }, "\x1b]4;1;rgb:12/34/56"},
		{"modes", func(x *TerminalExtra) { x.Modes = true }, "\x1b[?1h"},
		{"scrolling region", func(x *TerminalExtra) { x.ScrollingRegion = true }, "\x1b[2;5r"},
		{"tabstops", func(x *TerminalExtra) { x.Tabstops = true }, "\x1b[3g"},
		{"pwd", func(x *TerminalExtra) { x.Pwd = true }, "\x1b]7;file://h/tmp"},
		{"keyboard", func(x *TerminalExtra) { x.Keyboard = true }, "\x1b[>4;2m"},
		{"cursor", func(x *TerminalExtra) { x.Screen.Cursor = true }, "\x1b[3;4H"},
		{"style", func(x *TerminalExtra) { x.Screen.Style = true }, "\x1b[1m"},
		{"hyperlink", func(x *TerminalExtra) { x.Screen.Hyperlink = true }, "\x1b]8;;http://x"},
		{"protection", func(x *TerminalExtra) { x.Screen.Protection = true }, "\x1b[1\"q"},
		{"kitty keyboard", func(x *TerminalExtra) { x.Screen.KittyKeyboard = true }, "\x1b[=1;1u"},
		{"charsets", func(x *TerminalExtra) { x.Screen.Charsets = true }, "\x1b(0"},
	}
	require.Len(t, extras, bits.OnesCount32(allExtras.Bits()))
	outputs := make([]string, len(extras))
	for i, e := range extras {
		var x TerminalExtra
		e.set(&x)
		require.Equal(t, 1, bits.OnesCount32(x.Bits()), e.name)
		outputs[i] = format(t, in, FormatterOptions{Emit: FormatVT, ContentNone: true, Extra: x})
	}
	for i, e := range extras {
		for j, out := range outputs {
			if i == j {
				assert.Contains(t, out, e.marker, "%s alone", e.name)
			} else {
				assert.NotContains(t, out, e.marker, "%s alone emits %s", extras[j].name, e.name)
			}
		}
	}
}

// TestContentNoneKeepsSelection: clearing CONTENT_NONE, which was never
// set, must not drop the selection (patch 0001).
func TestContentNoneKeepsSelection(t *testing.T) {
	in := newTerm(t, 10, 3)
	feed(t, in, "ab\r\ncd")
	require.NoError(t, in.invoke("ghostty_formatter_terminal_new", func() int32 {
		return in.mod.Xghostty_formatter_terminal_new(0, int32(in.slot), int32(in.term))
	}))
	f, err := in.takeOpaque()
	require.NoError(t, err)
	defer in.mod.Xghostty_formatter_free(int32(f))

	require.NoError(t, in.setFormatterOptions(f, FormatterOptions{
		Emit: FormatPlain, Trim: true,
		Selection: &Selection{Start: Point{Tag: PointActive, Y: 1}, End: Point{Tag: PointActive, X: 9, Y: 1}},
	}))
	require.NoError(t, in.formatterSet(f, FmtOptContentNone, 0))
	out := in.scratch + scrOutPtr
	require.NoError(t, in.writeMem(out, zero8[:]))
	require.NoError(t, in.invoke("ghostty_formatter_format_alloc", func() int32 {
		return in.mod.Xghostty_formatter_format_alloc(int32(f), 0, int32(out), int32(in.scratch+scrOutLen))
	}))
	data, err := in.takeAlloc(out)
	require.NoError(t, err)
	assert.Equal(t, "cd", string(data))
}
