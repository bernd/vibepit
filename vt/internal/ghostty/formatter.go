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

// ScreenExtra is GhosttyFormatterScreenExtra.
type ScreenExtra struct {
	Cursor, Style, Hyperlink, Protection, KittyKeyboard, Charsets bool
}

// TerminalExtra is GhosttyFormatterTerminalExtra.
type TerminalExtra struct {
	Palette, Modes, ScrollingRegion, Tabstops, Pwd, Keyboard bool
	Screen                                                   ScreenExtra
}

// FormatterOptions is GhosttyFormatterTerminalOptions.
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
	ref := make([]byte, sizeofGridRef)
	le.PutUint32(ref[offGridRefSize:], sizeofGridRef)
	if err := in.writeMem(out, ref); err != nil {
		return err
	}
	return in.invoke("ghostty_terminal_grid_ref", func() int32 {
		return in.mod.Xghostty_terminal_grid_ref(int32(in.term), int32(in.scratch+scrPoint), int32(out))
	})
}

// FormatAlloc creates a formatter with opts, formats the terminal's current
// state once, and frees the formatter.
func (in *Instance) FormatAlloc(opts FormatterOptions) ([]byte, error) {
	var sel uint32
	if opts.Selection != nil && !opts.ContentNone {
		sel = in.scratch + scrSelection
		b := make([]byte, sizeofSelection)
		le.PutUint32(b[offSelectionSize:], sizeofSelection)
		if opts.Selection.Rectangle {
			b[offSelectionRectangle] = 1
		}
		if err := in.writeMem(sel, b); err != nil {
			return nil, err
		}
		if err := in.GridRef(opts.Selection.Start, sel+offSelectionStart); err != nil {
			return nil, err
		}
		if err := in.GridRef(opts.Selection.End, sel+offSelectionEnd); err != nil {
			return nil, err
		}
	}
	optPtr := in.scratch + scrFormatter
	if err := in.writeMem(optPtr, encodeFormatterOptions(opts, sel)); err != nil {
		return nil, err
	}
	if err := in.invoke("ghostty_formatter_terminal_new", func() int32 {
		return in.mod.Xghostty_formatter_terminal_new(0, int32(in.slot), int32(in.term), int32(optPtr))
	}); err != nil {
		return nil, err
	}
	f, err := in.takeOpaque()
	if err != nil {
		return nil, err
	}
	out := in.scratch + scrOutPtr
	if err := in.writeMem(out, zero8[:]); err != nil {
		return nil, err
	}
	err = in.invoke("ghostty_formatter_format_alloc", func() int32 {
		return in.mod.Xghostty_formatter_format_alloc(int32(f), 0, int32(out), int32(in.scratch+scrOutLen))
	})
	var data []byte
	if err == nil {
		data, err = in.takeAlloc(out)
	}
	if ferr := in.guard("ghostty_formatter_free", func() { in.mod.Xghostty_formatter_free(int32(f)) }); err == nil {
		err = ferr
	}
	return data, err
}

func encodeFormatterOptions(o FormatterOptions, sel uint32) []byte {
	b := make([]byte, sizeofFormatterOptions)
	le.PutUint32(b[offFmtSize:], sizeofFormatterOptions)
	le.PutUint32(b[offFmtEmit:], uint32(o.Emit))
	b[offFmtUnwrap] = b2u(o.Unwrap)
	b[offFmtTrim] = b2u(o.Trim)

	x := b[offFmtExtra:]
	le.PutUint32(x[offTExtraSize:], sizeofTerminalExtra)
	x[offTExtraPalette] = b2u(o.Extra.Palette)
	x[offTExtraModes] = b2u(o.Extra.Modes)
	x[offTExtraScrollRegion] = b2u(o.Extra.ScrollingRegion)
	x[offTExtraTabstops] = b2u(o.Extra.Tabstops)
	x[offTExtraPwd] = b2u(o.Extra.Pwd)
	x[offTExtraKeyboard] = b2u(o.Extra.Keyboard)

	s := x[offTExtraScreen:]
	le.PutUint32(s[offSExtraSize:], sizeofScreenExtra)
	s[offSExtraCursor] = b2u(o.Extra.Screen.Cursor)
	s[offSExtraStyle] = b2u(o.Extra.Screen.Style)
	s[offSExtraHyperlink] = b2u(o.Extra.Screen.Hyperlink)
	s[offSExtraProtection] = b2u(o.Extra.Screen.Protection)
	s[offSExtraKittyKeyboard] = b2u(o.Extra.Screen.KittyKeyboard)
	s[offSExtraCharsets] = b2u(o.Extra.Screen.Charsets)

	le.PutUint32(b[offFmtSelection:], sel)
	b[offFmtContentNone] = b2u(o.ContentNone)
	return b
}

func b2u(v bool) byte {
	if v {
		return 1
	}
	return 0
}
