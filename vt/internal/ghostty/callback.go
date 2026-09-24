package ghostty

// SetWritePty installs fn as the terminal's write_pty callback, which
// receives the answers to queries in the stream. nil removes it. fn runs
// synchronously inside VTWrite and must not call this instance. A panic in
// fn becomes a *TrapError from that VTWrite.
func (in *Instance) SetWritePty(fn func([]byte)) error {
	in.writePty = fn
	if fn == nil {
		return in.TerminalSetPtr(OptWritePty, 0)
	}
	if in.ptyIndex == 0 {
		// A C callback is a function table index, and an indirect call is a
		// type assertion on the entry, so the entry's type must match
		// GhosttyTerminalWritePtyFn exactly: (i32 i32 i32 i32) -> ().
		tab := in.mod.X__indirect_function_table()
		in.ptyIndex = uint32(len(*tab))
		*tab = append(*tab, in.onWritePty)
	}
	return in.TerminalSetPtr(OptWritePty, in.ptyIndex)
}

// onWritePty is the function table entry. Its arguments are the terminal,
// the userdata, and the answer's pointer and length in linear memory.
func (in *Instance) onWritePty(_, _, data, n int32) {
	if in.writePty == nil {
		return
	}
	// read copies: the bytes live in linear memory that the next call may
	// reuse. A bad pointer panics here, inside the guarded call.
	b, err := in.read(uint32(data), uint32(n))
	if err != nil {
		panic(err)
	}
	in.writePty(b)
}
