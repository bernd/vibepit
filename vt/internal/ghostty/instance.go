package ghostty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/bernd/vibepit/vt/internal/ghostty/internal/wasmvt"
)

var le = binary.LittleEndian

const (
	inputBufSize = 64 << 10

	// Scratch space for out-parameters and for structs passed by pointer.
	scrOut       = 0   // 8 bytes: out-parameter of *_get
	scrOutPtr    = 0   // (ptr, len) out-pair of the *_alloc functions
	scrOutLen    = 4   //
	scrPoint     = 16  // GhosttyPoint, 8-byte aligned
	scrSelection = 40  // GhosttySelection
	scrFormatter = 72  // GhosttyFormatterTerminalOptions
	scrMode      = 116 // GhosttyTerminalModeConfig
	scratchSize  = 128
)

var zero8 [8]byte

// Config configures NewInstance.
type Config struct {
	// MemoryLimitPages caps linear memory, in 64 KiB pages. Zero keeps the
	// module's limit of 4 GiB.
	MemoryLimitPages uint32
}

// Instance is one module instance holding at most one terminal. It is not
// safe for concurrent use.
type Instance struct {
	mod      *wasmvt.Module
	term     uint32 // GhosttyTerminal
	input    uint32 // reusable buffer for VT input
	scratch  uint32
	slot     uint32       // opaque out-parameter slot
	render   uint32       // GhosttyRenderState, created on first use
	ptyIndex uint32       // function table index of onWritePty
	writePty func([]byte) // see SetWritePty
}

// NewInstance creates a module instance: about 65 µs, most of it copying
// the data segment into fresh linear memory.
func NewInstance(cfg Config) (*Instance, error) {
	in := &Instance{}
	if err := in.guard("wasmvt.New", func() { in.mod = wasmvt.New() }); err != nil {
		return nil, err
	}
	if cfg.MemoryLimitPages > 0 {
		in.mod.SetMemoryLimitPages(int64(cfg.MemoryLimitPages))
	}
	if err := in.init(); err != nil {
		_ = in.Close()
		return nil, err
	}
	return in, nil
}

func (in *Instance) init() error {
	var err error
	if in.input, err = in.Alloc(inputBufSize); err != nil {
		return err
	}
	if in.scratch, err = in.Alloc(scratchSize); err != nil {
		return err
	}
	in.slot, err = in.callPtr("ghostty_wasm_alloc_opaque", func() int32 { return in.mod.Xghostty_wasm_alloc_opaque() })
	return err
}

// MemorySize is the current size of linear memory in bytes. It only grows.
func (in *Instance) MemorySize() uint64 { return uint64(len(in.mem())) }

// Close drops the instance. The garbage collector frees its linear memory,
// and the terminal in it. It is idempotent.
func (in *Instance) Close() error {
	in.mod = nil
	in.writePty = nil
	return nil
}

// guard runs f, which calls into the module, and turns a panic into a
// *TrapError. In translated code a WASM trap is a panic: a bounds check,
// unreachable, a function table entry of the wrong type. A panic in the
// write_pty callback surfaces here too. Runaway recursion is a fatal stack
// overflow instead, which no recover catches.
func (in *Instance) guard(name string, f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &TrapError{Func: name, Err: fmt.Errorf("%v", r)}
		}
	}()
	f()
	return nil
}

// call runs an export that returns an i32: a pointer, a handle or a size.
func (in *Instance) call(name string, f func() int32) (uint32, error) {
	var v int32
	err := in.guard(name, func() { v = f() })
	return uint32(v), err
}

// callPtr runs an export that returns a pointer, where NULL means it
// couldn't allocate.
func (in *Instance) callPtr(name string, f func() int32) (uint32, error) {
	p, err := in.call(name, f)
	if err == nil && p == 0 {
		err = &CallError{Func: name, Result: OutOfMemory}
	}
	return p, err
}

// callResult runs an export that returns a GhosttyResult.
func (in *Instance) callResult(name string, f func() int32) (Result, error) {
	v, err := in.call(name, f)
	return Result(int32(v)), err
}

// invoke runs an export that returns a GhosttyResult and treats anything
// but SUCCESS as an error.
func (in *Instance) invoke(name string, f func() int32) error {
	r, err := in.callResult(name, f)
	if err != nil {
		return err
	}
	if r != Success {
		return &CallError{Func: name, Result: r}
	}
	return nil
}

// takeOpaque is ghostty_wasm_take_opaque on the instance's slot: the handle
// a *_new function just wrote there.
func (in *Instance) takeOpaque() (uint32, error) {
	return in.call("ghostty_wasm_take_opaque", func() int32 {
		return in.mod.Xghostty_wasm_take_opaque(int32(in.slot))
	})
}

// Alloc is ghostty_wasm_alloc.
func (in *Instance) Alloc(n uint32) (uint32, error) {
	return in.callPtr("ghostty_wasm_alloc", func() int32 { return in.mod.Xghostty_wasm_alloc(int32(n)) })
}

// Free is ghostty_wasm_free.
func (in *Instance) Free(p, n uint32) error {
	return in.guard("ghostty_wasm_free", func() { in.mod.Xghostty_wasm_free(int32(p), int32(n)) })
}

// mem is linear memory. A call can grow it and move the slice, so never
// keep it across a call into the module.
func (in *Instance) mem() []byte { return *in.mod.Xmemory().Slice() }

// span returns n bytes of linear memory at p, or a *TrapError for a
// pointer from the module that points outside it.
func (in *Instance) span(p, n uint32) ([]byte, error) {
	m := in.mem()
	if uint64(p)+uint64(n) > uint64(len(m)) {
		return nil, &TrapError{Func: "memory", Err: errOutOfBounds}
	}
	return m[p : p+n], nil
}

// read copies n bytes out of linear memory. The copy matters: the next
// call may grow memory or reuse the bytes.
func (in *Instance) read(p, n uint32) ([]byte, error) {
	b, err := in.span(p, n)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(b), nil
}

func (in *Instance) writeMem(p uint32, b []byte) error {
	dst, err := in.span(p, uint32(len(b)))
	if err != nil {
		return err
	}
	copy(dst, b)
	return nil
}

func (in *Instance) readU16(p uint32) (uint16, error) {
	b, err := in.span(p, 2)
	if err != nil {
		return 0, err
	}
	return le.Uint16(b), nil
}

func (in *Instance) readU32(p uint32) (uint32, error) {
	b, err := in.span(p, 4)
	if err != nil {
		return 0, err
	}
	return le.Uint32(b), nil
}

func (in *Instance) writeU32(p, v uint32) error {
	b, err := in.span(p, 4)
	if err != nil {
		return err
	}
	le.PutUint32(b, v)
	return nil
}

func (in *Instance) readByte(p uint32) (byte, error) {
	b, err := in.span(p, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// TerminalNew is ghostty_terminal_new with the default allocator.
func (in *Instance) TerminalNew(cols, rows uint16) error {
	if in.term != 0 {
		return errors.New("ghostty: instance already holds a terminal")
	}
	if err := in.invoke("ghostty_terminal_new", func() int32 {
		return in.mod.Xghostty_terminal_new(0, int32(in.slot), int32(cols), int32(rows))
	}); err != nil {
		return err
	}
	h, err := in.takeOpaque()
	if err != nil {
		return err
	}
	in.term = h
	return nil
}

// TerminalFree is ghostty_terminal_free. It also frees the render state,
// which borrows from the terminal.
func (in *Instance) TerminalFree() error {
	if in.term == 0 {
		return nil
	}
	if in.render != 0 {
		if err := in.guard("ghostty_render_state_free", func() {
			in.mod.Xghostty_render_state_free(int32(in.render))
		}); err != nil {
			return err
		}
		in.render = 0
	}
	err := in.guard("ghostty_terminal_free", func() { in.mod.Xghostty_terminal_free(int32(in.term)) })
	in.term = 0
	return err
}

// TerminalReset is ghostty_terminal_reset (RIS).
func (in *Instance) TerminalReset() error {
	return in.guard("ghostty_terminal_reset", func() { in.mod.Xghostty_terminal_reset(int32(in.term)) })
}

// TerminalResize is ghostty_terminal_resize.
func (in *Instance) TerminalResize(cols, rows uint16, cellWidthPx, cellHeightPx uint32) error {
	return in.invoke("ghostty_terminal_resize", func() int32 {
		return in.mod.Xghostty_terminal_resize(int32(in.term), int32(cols), int32(rows),
			int32(cellWidthPx), int32(cellHeightPx))
	})
}

// TerminalSetPtr is ghostty_terminal_set with a raw value: a callback's
// table index, or 0 for NULL.
func (in *Instance) TerminalSetPtr(opt TerminalOption, v uint32) error {
	return in.invoke("ghostty_terminal_set", func() int32 {
		return in.mod.Xghostty_terminal_set(int32(in.term), int32(opt), int32(v))
	})
}

// TerminalSetSize sets an option whose input type is size_t*.
func (in *Instance) TerminalSetSize(opt TerminalOption, v uint32) error {
	p := in.scratch + scrOut
	if err := in.writeU32(p, v); err != nil {
		return err
	}
	return in.TerminalSetPtr(opt, p)
}

// TerminalSetMode sets GHOSTTY_TERMINAL_OPT_MODE.
func (in *Instance) TerminalSetMode(mode uint16, value bool) error {
	p := in.scratch + scrMode
	if err := in.putModeConfig(p, mode, value); err != nil {
		return err
	}
	return in.TerminalSetPtr(OptMode, p)
}

// putModeConfig writes a GhosttyTerminalModeConfig at p.
func (in *Instance) putModeConfig(p uint32, mode uint16, value bool) error {
	b, err := in.span(p, sizeofModeConfig)
	if err != nil {
		return err
	}
	clear(b)
	le.PutUint16(b[offModeConfigMode:], mode)
	if value {
		b[offModeConfigValue] = 1
	}
	return nil
}

// getOut runs a *_get export f into zeroed scratch and returns where the
// value is.
func (in *Instance) getOut(name string, f func(out int32) int32) (uint32, error) {
	p := in.scratch + scrOut
	if err := in.writeMem(p, zero8[:]); err != nil {
		return 0, err
	}
	return p, in.invoke(name, func() int32 { return f(int32(p)) })
}

func (in *Instance) get(data TerminalData) (uint32, error) {
	return in.getOut("ghostty_terminal_get", func(out int32) int32 {
		return in.mod.Xghostty_terminal_get(int32(in.term), int32(data), out)
	})
}

// GetU8 reads a uint8_t value.
func (in *Instance) GetU8(d TerminalData) (uint8, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	return in.readByte(p)
}

// GetU16 reads a uint16_t value.
func (in *Instance) GetU16(d TerminalData) (uint16, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	return in.readU16(p)
}

// GetU32 reads a uint32_t, size_t or enum value.
func (in *Instance) GetU32(d TerminalData) (uint32, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	return in.readU32(p)
}

// GetBool reads a bool value.
func (in *Instance) GetBool(d TerminalData) (bool, error) {
	v, err := in.GetU8(d)
	return v != 0, err
}

// GetString reads a GhosttyString value and copies the borrowed bytes.
func (in *Instance) GetString(d TerminalData) (string, error) {
	p, err := in.get(d)
	if err != nil {
		return "", err
	}
	b, err := in.read(p, sizeofString)
	if err != nil {
		return "", err
	}
	ptr, n := le.Uint32(b[offStringPtr:]), le.Uint32(b[offStringLen:])
	if n == 0 {
		return "", nil
	}
	s, err := in.read(ptr, n)
	return string(s), err
}

// GetMode reads GHOSTTY_TERMINAL_DATA_MODE. An unknown mode is
// InvalidValue.
func (in *Instance) GetMode(mode uint16) (bool, error) {
	p := in.scratch + scrMode
	if err := in.putModeConfig(p, mode, false); err != nil {
		return false, err
	}
	if err := in.invoke("ghostty_terminal_get", func() int32 {
		return in.mod.Xghostty_terminal_get(int32(in.term), int32(DataMode), int32(p))
	}); err != nil {
		return false, err
	}
	v, err := in.readByte(p + offModeConfigValue)
	return v != 0, err
}

// VTWrite is ghostty_terminal_vt_write, in chunks of the input buffer.
func (in *Instance) VTWrite(p []byte) error {
	for len(p) > 0 {
		n, err := in.stage(p)
		if err != nil {
			return err
		}
		if err := in.guard("ghostty_terminal_vt_write", func() {
			in.mod.Xghostty_terminal_vt_write(int32(in.term), int32(in.input), int32(n))
		}); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// stage copies as much of p as fits into the input buffer.
func (in *Instance) stage(p []byte) (int, error) {
	n := min(len(p), inputBufSize)
	return n, in.writeMem(in.input, p[:n])
}

// TypeJSON is ghostty_type_json.
func (in *Instance) TypeJSON() (string, error) {
	p, err := in.call("ghostty_type_json", func() int32 { return in.mod.Xghostty_type_json() })
	if err != nil {
		return "", err
	}
	b, err := in.span(p, uint32(len(in.mem()))-p)
	if err != nil {
		return "", err
	}
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return "", errors.New("ghostty: type json is not NUL-terminated")
	}
	return string(b[:end]), nil
}
