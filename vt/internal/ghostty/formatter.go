package ghostty

// Point is GhosttyPoint with a coordinate value.
type Point struct {
	Tag PointTag
	X   uint16
	Y   uint32
}

// Selection is GhosttySelection, given as two points. FormatAlloc turns
// them into grid references.
type Selection struct {
	Start, End Point
	Rectangle  bool
}

// ScreenExtra holds the screen bits of GhosttyFormatterExtras.
type ScreenExtra struct {
	Cursor, Style, Hyperlink, Protection, KittyKeyboard, Charsets bool
}

// TerminalExtra is GhosttyFormatterExtras as booleans.
type TerminalExtra struct {
	Palette, Modes, ScrollingRegion, Tabstops, Pwd, Keyboard bool
	Screen                                                   ScreenExtra
}

// Bits returns the GhosttyFormatterExtras bitmask.
func (x TerminalExtra) Bits() uint32 {
	var b uint32
	for _, f := range []struct {
		on  bool
		bit uint32
	}{
		{x.Palette, extraPalette},
		{x.Modes, extraModes},
		{x.ScrollingRegion, extraScrollingRegion},
		{x.Tabstops, extraTabstops},
		{x.Pwd, extraPwd},
		{x.Keyboard, extraKeyboard},
		{x.Screen.Cursor, extraCursor},
		{x.Screen.Style, extraStyle},
		{x.Screen.Hyperlink, extraHyperlink},
		{x.Screen.Protection, extraProtection},
		{x.Screen.KittyKeyboard, extraKittyKeyboard},
		{x.Screen.Charsets, extraCharsets},
	} {
		if f.on {
			b |= f.bit
		}
	}
	return b
}

// FormatterOptions are the GhosttyFormatterOption values FormatAlloc sets.
type FormatterOptions struct {
	Emit         FormatterFormat
	Unwrap, Trim bool
	Extra        TerminalExtra
	Selection    *Selection // nil formats the whole screen, scrollback included
	ContentNone  bool       // extras only; overrides Selection
}

// GridRef is ghostty_terminal_grid_ref, writing the GhosttyGridRef to out.
func (in *Instance) GridRef(pt Point, out uint32) error {
	p := make([]byte, sizeofPoint)
	le.PutUint32(p[offPointTag:], uint32(pt.Tag))
	le.PutUint16(p[offPointValue+offCoordinateX:], pt.X)
	le.PutUint32(p[offPointValue+offCoordinateY:], pt.Y)
	if err := in.writeMem(in.scratch+scrPoint, p); err != nil {
		return err
	}
	return in.invoke("ghostty_terminal_grid_ref", func() int32 {
		return in.mod.Xghostty_terminal_grid_ref(int32(in.term), int32(in.scratch+scrPoint), int32(out))
	})
}

// FormatAlloc creates a formatter with opts, formats the terminal's current
// state once, and frees the formatter.
func (in *Instance) FormatAlloc(opts FormatterOptions) ([]byte, error) {
	if err := in.invoke("ghostty_formatter_terminal_new", func() int32 {
		return in.mod.Xghostty_formatter_terminal_new(0, int32(in.slot), int32(in.term))
	}); err != nil {
		return nil, err
	}
	f, err := in.takeOpaque()
	if err != nil {
		return nil, err
	}
	var data []byte
	err = in.setFormatterOptions(f, opts)
	if err == nil {
		out := in.scratch + scrOutPtr
		if err = in.writeMem(out, zero8[:]); err == nil {
			err = in.invoke("ghostty_formatter_format_alloc", func() int32 {
				return in.mod.Xghostty_formatter_format_alloc(int32(f), 0, int32(out), int32(in.scratch+scrOutLen))
			})
		}
		if err == nil {
			data, err = in.takeAlloc(out)
		}
	}
	if ferr := in.guard("ghostty_formatter_free", func() { in.mod.Xghostty_formatter_free(int32(f)) }); err == nil {
		err = ferr
	}
	return data, err
}

// formatterSetPtr is ghostty_formatter_set with the value at p.
func (in *Instance) formatterSetPtr(f uint32, opt FormatterOption, p uint32) error {
	return in.invoke("ghostty_formatter_set", func() int32 {
		return in.mod.Xghostty_formatter_set(int32(f), int32(opt), int32(p))
	})
}

// formatterSet is ghostty_formatter_set with a 4-byte value: an enum, a
// bitmask, or a bool, which reads only the low byte.
func (in *Instance) formatterSet(f uint32, opt FormatterOption, v uint32) error {
	val := in.scratch + scrValue
	if err := in.writeU32(val, v); err != nil {
		return err
	}
	return in.formatterSetPtr(f, opt, val)
}

func (in *Instance) setFormatterOptions(f uint32, opts FormatterOptions) error {
	for _, o := range []struct {
		opt FormatterOption
		v   uint32
	}{
		{FmtOptEmit, uint32(opts.Emit)},
		{FmtOptUnwrap, uint32(b2u(opts.Unwrap))},
		{FmtOptTrim, uint32(b2u(opts.Trim))},
		{FmtOptExtras, opts.Extra.Bits()},
	} {
		if err := in.formatterSet(f, o.opt, o.v); err != nil {
			return err
		}
	}
	if opts.ContentNone {
		return in.formatterSet(f, FmtOptContentNone, 1)
	}
	if opts.Selection == nil {
		return nil
	}
	sel := in.scratch + scrSelection
	b := make([]byte, sizeofSelection)
	if opts.Selection.Rectangle {
		b[offSelectionRectangle] = 1
	}
	if err := in.writeMem(sel, b); err != nil {
		return err
	}
	if err := in.GridRef(opts.Selection.Start, sel+offSelectionStart); err != nil {
		return err
	}
	if err := in.GridRef(opts.Selection.End, sel+offSelectionEnd); err != nil {
		return err
	}
	return in.formatterSetPtr(f, FmtOptSelection, sel)
}

func b2u(v bool) byte {
	if v {
		return 1
	}
	return 0
}
