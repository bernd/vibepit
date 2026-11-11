package vt

import (
	"fmt"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

// Region selects the content Format emits.
type Region uint8

const (
	// RegionScreen is the visible screen.
	RegionScreen Region = iota
	// RegionScrollback is the history above the visible screen.
	RegionScrollback
	// RegionNone emits no content, only the requested extras.
	RegionNone
)

// Output selects the output format.
type Output uint8

const (
	OutputVT Output = iota
	OutputPlain
)

// Extras selects the terminal state Format emits besides the content. The
// emulator's palette, tab-stop and working-directory extras are left out:
// the tab-stop extra moves the cursor and the working-directory extra
// emits a stray NUL.
type Extras struct {
	Modes         bool // modes that differ from their defaults
	ScrollRegion  bool
	Keyboard      bool // modifyOtherKeys
	Cursor        bool // position, including a pending wrap
	Style         bool // the SGR pen
	Hyperlink     bool // the pen's open OSC 8 link
	Protection    bool
	KittyKeyboard bool
	Charsets      bool
}

// AllExtras emits every supported extra.
var AllExtras = Extras{
	Modes: true, ScrollRegion: true, Keyboard: true, Cursor: true, Style: true,
	Hyperlink: true, Protection: true, KittyKeyboard: true, Charsets: true,
}

// FormatOptions configures Format.
type FormatOptions struct {
	Output Output
	// Unwrap joins soft-wrapped lines, so a replay at another width
	// reflows them.
	Unwrap bool
	// Trim drops trailing whitespace on non-blank lines.
	Trim   bool
	Extras Extras
	Region Region
}

// Format serializes the terminal's current state. Modes are emitted before
// the content; the other extras follow it, in an order that restores
// them: the scroll region, modifyOtherKeys, the cursor, the pen, the
// hyperlink, protection, the kitty keyboard flags, then the charsets.
//
// The extras always describe the live screen, whatever the region. With
// RegionScrollback the cursor extra is still the screen's cursor
// position, not a position within the history: on a 10x3 terminal fed
// "a\r\nb\r\nc\r\nd\r\ne", the scrollback with AllExtras is
// "a\r\nb\x1b[3;2H\x1b[0m".
func (t *Terminal) Format(opts FormatOptions) ([]byte, error) {
	return query(t, func(in *ghostty.Instance) ([]byte, error) {
		o, err := formatterOptions(in, opts)
		if err != nil {
			return nil, err
		}
		return in.FormatAlloc(o)
	})
}

func formatterOptions(in *ghostty.Instance, opts FormatOptions) (ghostty.FormatterOptions, error) {
	x := opts.Extras
	o := ghostty.FormatterOptions{
		Emit:   ghostty.FormatVT,
		Unwrap: opts.Unwrap,
		Trim:   opts.Trim,
		Extra: ghostty.TerminalExtra{
			Modes:           x.Modes,
			ScrollingRegion: x.ScrollRegion,
			Keyboard:        x.Keyboard,
			Screen: ghostty.ScreenExtra{
				Cursor:        x.Cursor,
				Style:         x.Style,
				Hyperlink:     x.Hyperlink,
				Protection:    x.Protection,
				KittyKeyboard: x.KittyKeyboard,
				Charsets:      x.Charsets,
			},
		},
	}
	if opts.Output == OutputPlain {
		o.Emit = ghostty.FormatPlain
	}
	cols, err := in.GetU16(ghostty.DataCols)
	if err != nil {
		return o, err
	}
	switch opts.Region {
	case RegionNone:
		o.ContentNone = true
	case RegionScreen:
		rows, err := in.GetU16(ghostty.DataRows)
		if err != nil {
			return o, err
		}
		o.Selection = &ghostty.Selection{
			Start: ghostty.Point{Tag: ghostty.PointActive},
			End:   ghostty.Point{Tag: ghostty.PointActive, X: cols - 1, Y: uint32(rows) - 1},
		}
	case RegionScrollback:
		rows, err := in.GetU32(ghostty.DataScrollbackRows)
		if err != nil {
			return o, err
		}
		if rows == 0 {
			// No history: a selection can't be empty.
			o.ContentNone = true
			break
		}
		o.Selection = &ghostty.Selection{
			Start: ghostty.Point{Tag: ghostty.PointHistory},
			End:   ghostty.Point{Tag: ghostty.PointHistory, X: cols - 1, Y: rows - 1},
		}
	default:
		return o, fmt.Errorf("vt: unknown region %d", opts.Region)
	}
	return o, nil
}
