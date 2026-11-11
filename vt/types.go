package vt

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

// ModeState is one terminal mode and its current value.
type ModeState struct {
	Mode  uint16
	ANSI  bool // an ANSI mode (CSI n h) rather than a DEC mode (CSI ? n h)
	Value bool
}

// CursorShape is the shape selected by DECSCUSR.
type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorBar
	CursorUnderline
	CursorBlockHollow
)

// CursorStyle is the cursor's DECSCUSR state.
type CursorStyle struct {
	Shape    CursorShape
	Blinking bool
}

// DECSCUSR returns the sequence that selects s. A hollow block has no
// DECSCUSR code and maps to a block.
func (s CursorStyle) DECSCUSR() string {
	n := 2
	switch s.Shape {
	case CursorUnderline:
		n = 4
	case CursorBar:
		n = 6
	}
	if s.Blinking {
		n--
	}
	return ansi.SetCursorStyle(n)
}

func cursorShape(v ghostty.CursorVisualStyle) CursorShape {
	switch v {
	case ghostty.CursorVisualBar:
		return CursorBar
	case ghostty.CursorVisualUnderline:
		return CursorUnderline
	case ghostty.CursorVisualBlockHollow:
		return CursorBlockHollow
	default:
		return CursorBlock
	}
}

// MouseTracking is the most inclusive mouse tracking mode that is on.
type MouseTracking uint8

const (
	MouseNone   MouseTracking = iota
	MouseX10                  // mode 9
	MouseNormal               // mode 1000
	MouseButton               // mode 1002
	MouseAny                  // mode 1003
)
