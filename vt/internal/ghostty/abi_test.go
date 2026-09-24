package ghostty

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestABIMatchesTypeJSON compares every hard-coded layout and enum value
// with the module's own description. A ghostty bump that changes one fails
// here, not in production.
func TestABIMatchesTypeJSON(t *testing.T) {
	in, err := NewInstance(Config{})
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
			"size": offGridRefSize, "node": offGridRefNode, "x": offGridRefX, "y": offGridRefY,
		}},
		{"GhosttySelection", sizeofSelection, map[string]uint32{
			"size": offSelectionSize, "start": offSelectionStart, "end": offSelectionEnd, "rectangle": offSelectionRectangle,
		}},
		{"GhosttyFormatterTerminalOptions", sizeofFormatterOptions, map[string]uint32{
			"size": offFmtSize, "emit": offFmtEmit, "unwrap": offFmtUnwrap, "trim": offFmtTrim,
			"extra": offFmtExtra, "selection": offFmtSelection, "content_none": offFmtContentNone,
		}},
		{"GhosttyFormatterTerminalExtra", sizeofTerminalExtra, map[string]uint32{
			"size": offTExtraSize, "palette": offTExtraPalette, "modes": offTExtraModes,
			"scrolling_region": offTExtraScrollRegion, "tabstops": offTExtraTabstops,
			"pwd": offTExtraPwd, "keyboard": offTExtraKeyboard, "screen": offTExtraScreen,
		}},
		{"GhosttyFormatterScreenExtra", sizeofScreenExtra, map[string]uint32{
			"size": offSExtraSize, "cursor": offSExtraCursor, "style": offSExtraStyle,
			"hyperlink": offSExtraHyperlink, "protection": offSExtraProtection,
			"kitty_keyboard": offSExtraKittyKeyboard, "charsets": offSExtraCharsets,
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
		{"GhosttyPointTag", "ACTIVE", int64(PointActive)},
		{"GhosttyPointTag", "VIEWPORT", int64(PointViewport)},
		{"GhosttyPointTag", "SCREEN", int64(PointScreen)},
		{"GhosttyPointTag", "HISTORY", int64(PointHistory)},
		{"GhosttyRenderStateData", "CURSOR_VISUAL_STYLE", int64(RenderDataCursorVisualStyle)},
		{"GhosttyRenderStateData", "CURSOR_BLINKING", int64(RenderDataCursorBlinking)},
		{"GhosttyRenderStateCursorVisualStyle", "BAR", int64(CursorVisualBar)},
		{"GhosttyRenderStateCursorVisualStyle", "BLOCK", int64(CursorVisualBlock)},
		{"GhosttyRenderStateCursorVisualStyle", "UNDERLINE", int64(CursorVisualUnderline)},
		{"GhosttyRenderStateCursorVisualStyle", "BLOCK_HOLLOW", int64(CursorVisualBlockHollow)},
	}
	for _, e := range enums {
		v, ok := doc.Types[e.typ].Values[e.name]
		if assert.True(t, ok, "%s.%s missing", e.typ, e.name) {
			assert.Equal(t, e.want, v, "%s.%s", e.typ, e.name)
		}
	}
}
