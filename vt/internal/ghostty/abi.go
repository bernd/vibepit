package ghostty

import "fmt"

// The values in this file mirror include/ghostty/vt/*.h and
// src/terminal/modes.zig at the commit in GHOSTTY_COMMIT, for wasm32.
// TestABIMatchesTypeJSON and TestModeTableComplete check them against the
// translated module.

// Result is GhosttyResult.
type Result int32

const (
	Success       Result = 0
	OutOfMemory   Result = -1
	InvalidValue  Result = -2
	OutOfSpace    Result = -3
	NoValue       Result = -4
	IOError       Result = -5
	LimitExceeded Result = -6
	Rejected      Result = -7
)

var resultNames = map[Result]string{
	Success:       "SUCCESS",
	OutOfMemory:   "OUT_OF_MEMORY",
	InvalidValue:  "INVALID_VALUE",
	OutOfSpace:    "OUT_OF_SPACE",
	NoValue:       "NO_VALUE",
	IOError:       "IO_ERROR",
	LimitExceeded: "LIMIT_EXCEEDED",
	Rejected:      "REJECTED",
}

func (r Result) String() string {
	if n, ok := resultNames[r]; ok {
		return n
	}
	return fmt.Sprintf("result %d", int32(r))
}

func (r Result) Error() string { return "ghostty: " + r.String() }

// TerminalOption is GhosttyTerminalOption.
type TerminalOption int32

const (
	OptWritePty             TerminalOption = 1
	OptScrollbackMaxBytes   TerminalOption = 27
	OptScrollbackMaxLines   TerminalOption = 28
	OptContinuationMaxBytes TerminalOption = 31
	OptMode                 TerminalOption = 34
)

// TerminalData is GhosttyTerminalData.
type TerminalData int32

const (
	DataCols                 TerminalData = 1
	DataRows                 TerminalData = 2
	DataCursorX              TerminalData = 3
	DataCursorY              TerminalData = 4
	DataCursorPendingWrap    TerminalData = 5
	DataActiveScreen         TerminalData = 6
	DataCursorVisible        TerminalData = 7
	DataKittyKeyboardFlags   TerminalData = 8
	DataMouseTracking        TerminalData = 11
	DataTitle                TerminalData = 12
	DataPwd                  TerminalData = 13
	DataTotalRows            TerminalData = 14
	DataScrollbackRows       TerminalData = 15
	DataScrollbackMaxBytes   TerminalData = 34
	DataScrollbackMaxLines   TerminalData = 35
	DataContinuationMaxBytes TerminalData = 36
	DataMode                 TerminalData = 37
	DataVTGround             TerminalData = 38
)

// GhosttyTerminalScreen values, as returned for DataActiveScreen.
const (
	ScreenPrimary   int32 = 0
	ScreenAlternate int32 = 1
)

// FormatterFormat is GhosttyFormatterFormat.
type FormatterFormat int32

const (
	FormatPlain FormatterFormat = 0
	FormatVT    FormatterFormat = 1
	FormatHTML  FormatterFormat = 2
)

// PointTag is GhosttyPointTag.
type PointTag int32

const (
	PointActive   PointTag = 0
	PointViewport PointTag = 1
	PointScreen   PointTag = 2
	PointHistory  PointTag = 3
)

// RenderStateData is GhosttyRenderStateData.
type RenderStateData int32

const (
	RenderDataCursorVisualStyle RenderStateData = 10
	RenderDataCursorBlinking    RenderStateData = 12
)

// CursorVisualStyle is GhosttyRenderStateCursorVisualStyle.
type CursorVisualStyle int32

const (
	CursorVisualBar         CursorVisualStyle = 0
	CursorVisualBlock       CursorVisualStyle = 1
	CursorVisualUnderline   CursorVisualStyle = 2
	CursorVisualBlockHollow CursorVisualStyle = 3
)

// Struct sizes and field offsets on wasm32.
const (
	sizeofString = 8
	offStringPtr = 0
	offStringLen = 4

	sizeofModeConfig   = 4
	offModeConfigMode  = 0
	offModeConfigValue = 2

	sizeofPoint      = 24
	offPointTag      = 0
	offPointValue    = 8
	sizeofCoordinate = 8
	offCoordinateX   = 0
	offCoordinateY   = 4

	sizeofGridRef  = 12
	offGridRefSize = 0
	offGridRefNode = 4 // opaque; GridRef only writes size
	offGridRefX    = 8
	offGridRefY    = 10

	sizeofSelection       = 32
	offSelectionSize      = 0
	offSelectionStart     = 4
	offSelectionEnd       = 16
	offSelectionRectangle = 28

	sizeofFormatterOptions = 44
	offFmtSize             = 0
	offFmtEmit             = 4
	offFmtUnwrap           = 8
	offFmtTrim             = 9
	offFmtExtra            = 12
	offFmtSelection        = 36
	offFmtContentNone      = 40 // patches/0001-formatter-content-none.patch

	sizeofTerminalExtra   = 24
	offTExtraSize         = 0
	offTExtraPalette      = 4
	offTExtraModes        = 5
	offTExtraScrollRegion = 6
	offTExtraTabstops     = 7
	offTExtraPwd          = 8
	offTExtraKeyboard     = 9
	offTExtraScreen       = 12

	sizeofScreenExtra      = 12
	offSExtraSize          = 0
	offSExtraCursor        = 4
	offSExtraStyle         = 5
	offSExtraHyperlink     = 6
	offSExtraProtection    = 7
	offSExtraKittyKeyboard = 8
	offSExtraCharsets      = 9
)

// ModeEntry is one entry of the mode table in src/terminal/modes.zig.
type ModeEntry struct {
	Value   uint16
	ANSI    bool
	Default bool
}

// Mode returns the entry as a GhosttyMode.
func (m ModeEntry) Mode() uint16 { return EncodeMode(m.Value, m.ANSI) }

// EncodeMode builds a GhosttyMode, like ghostty_mode_new.
func EncodeMode(value uint16, ansi bool) uint16 {
	v := value & 0x7fff
	if ansi {
		v |= 0x8000
	}
	return v
}

// Modes lists every mode libghostty knows, in modes.zig order.
var Modes = []ModeEntry{
	{2, true, false},     // KAM
	{4, true, false},     // IRM
	{12, true, true},     // SRM
	{20, true, false},    // LNM
	{1, false, false},    // DECCKM
	{3, false, false},    // DECCOLM
	{4, false, false},    // slow scroll
	{5, false, false},    // DECSCNM
	{6, false, false},    // DECOM
	{7, false, true},     // DECAWM
	{8, false, false},    // autorepeat
	{9, false, false},    // X10 mouse
	{12, false, false},   // cursor blinking
	{25, false, true},    // DECTCEM
	{40, false, false},   // allow 132 columns
	{45, false, false},   // reverse wrap
	{47, false, false},   // alt screen (legacy)
	{66, false, false},   // DECNKM
	{67, false, false},   // DECBKM
	{69, false, false},   // DECLRMM
	{1000, false, false}, // normal mouse
	{1002, false, false}, // button mouse
	{1003, false, false}, // any mouse
	{1004, false, false}, // focus events
	{1005, false, false}, // UTF-8 mouse
	{1006, false, false}, // SGR mouse
	{1007, false, true},  // alternate scroll
	{1015, false, false}, // urxvt mouse
	{1016, false, false}, // SGR-pixels mouse
	{1035, false, true},  // ignore keypad with NumLock
	{1036, false, true},  // alt sends ESC prefix
	{1039, false, false}, // alt sends escape
	{1045, false, false}, // extended reverse wrap
	{1047, false, false}, // alt screen
	{1048, false, false}, // save cursor
	{1049, false, false}, // alt screen + save cursor + clear
	{2004, false, false}, // bracketed paste
	{2026, false, false}, // synchronized output
	{2027, false, false}, // grapheme clusters
	{2031, false, false}, // color scheme reports
	{2033, false, false}, // visibility reports
	{2048, false, false}, // in-band size reports
	{5522, false, false}, // kitty paste events
}
