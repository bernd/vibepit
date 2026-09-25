// Package vt emulates a terminal as a shadow of a byte stream: it parses
// everything a program writes, reports the resulting terminal state, and
// serializes that state back to VT sequences.
package vt

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

var (
	// ErrFailed wraps the fault that stopped a terminal. Every later call
	// returns it; callers fall back to working without the emulator.
	ErrFailed = errors.New("vt: terminal failed")
	// ErrClosed is returned by calls after Close.
	ErrClosed = errors.New("vt: terminal closed")
	// ErrContinuationUnavailable means the unfinished sequence outgrew
	// the continuation limit, or tracking is off.
	ErrContinuationUnavailable = errors.New("vt: continuation unavailable")
	// ErrOutOfMemory means the emulator couldn't allocate under its memory
	// limit, typically for a large Format. The terminal stays usable, and
	// smaller requests (RegionScreen) still succeed.
	ErrOutOfMemory = errors.New("vt: emulator out of memory")

	errSize = errors.New("vt: terminal size must be at least 1x1")
)

// Defaults for NewTerminal. The emulator's own defaults are not relied on.
const (
	DefaultMemoryLimit          = 256 << 20
	DefaultScrollbackLines      = 1000
	DefaultScrollbackBytes      = 32 << 20
	DefaultContinuationMaxBytes = 64 << 10
)

const pageSize = 64 << 10

type terminalConfig struct {
	memoryLimit     uint64
	scrollbackLines uint
	scrollbackBytes uint
	continuationMax uint
	writePty        func([]byte)
}

// TerminalOption configures NewTerminal.
type TerminalOption func(*terminalConfig)

// WithMemoryLimit caps the terminal's emulator memory, rounded up to 64 KiB.
// Past the cap, Write keeps working and the emulator drops history instead
// of growing, but a Format whose output doesn't fit fails with
// ErrOutOfMemory (a RegionScrollback of a long history, for example). Keep
// the cap well above WithScrollbackBytes. Zero means no cap below the
// emulator's 4 GiB address space.
func WithMemoryLimit(bytes uint64) TerminalOption {
	return func(c *terminalConfig) { c.memoryLimit = bytes }
}

// WithScrollbackLines limits history to about n lines. The limit applies
// per page of rows, so a little less may be kept.
func WithScrollbackLines(n uint) TerminalOption {
	return func(c *terminalConfig) { c.scrollbackLines = n }
}

// WithScrollbackBytes limits history memory to about n bytes. Zero keeps
// no history.
func WithScrollbackBytes(n uint) TerminalOption {
	return func(c *terminalConfig) { c.scrollbackBytes = n }
}

// WithContinuationMaxBytes bounds how much of an unfinished escape
// sequence Continuation can reproduce. Zero turns tracking off.
func WithContinuationMaxBytes(n uint) TerminalOption {
	return func(c *terminalConfig) { c.continuationMax = n }
}

// WithWritePty receives the terminal's answers to queries in the stream.
// fn runs synchronously inside Write while the terminal is locked, so it
// must not call the terminal. It may keep the slice.
func WithWritePty(fn func([]byte)) TerminalOption {
	return func(c *terminalConfig) { c.writePty = fn }
}

// Terminal is one emulated terminal. It is safe for concurrent use.
type Terminal struct {
	mu     sync.Mutex
	inst   *ghostty.Instance
	failed error
}

// NewTerminal creates a terminal of cols x rows cells. It takes about
// 65 µs; there is nothing to set up beforehand.
func NewTerminal(cols, rows uint16, opts ...TerminalOption) (*Terminal, error) {
	if cols == 0 || rows == 0 {
		return nil, errSize
	}
	cfg := terminalConfig{
		memoryLimit:     DefaultMemoryLimit,
		scrollbackLines: DefaultScrollbackLines,
		scrollbackBytes: DefaultScrollbackBytes,
		continuationMax: DefaultContinuationMaxBytes,
	}
	for _, o := range opts {
		o(&cfg)
	}
	pages := min((cfg.memoryLimit+pageSize-1)/pageSize, 65536)
	in, err := ghostty.NewInstance(ghostty.Config{MemoryLimitPages: uint32(pages)})
	if err != nil {
		return nil, fmt.Errorf("vt: %w", err)
	}
	if err := configure(in, cols, rows, cfg); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("vt: %w", err)
	}
	return &Terminal{inst: in}, nil
}

func configure(in *ghostty.Instance, cols, rows uint16, cfg terminalConfig) error {
	if err := in.TerminalNew(cols, rows); err != nil {
		return err
	}
	for _, o := range []struct {
		opt ghostty.TerminalOption
		v   uint
	}{
		{ghostty.OptScrollbackMaxLines, cfg.scrollbackLines},
		{ghostty.OptScrollbackMaxBytes, cfg.scrollbackBytes},
		{ghostty.OptContinuationMaxBytes, cfg.continuationMax},
	} {
		if err := in.TerminalSetSize(o.opt, uint32(min(o.v, math.MaxUint32))); err != nil {
			return err
		}
	}
	if cfg.writePty != nil {
		return in.SetWritePty(cfg.writePty)
	}
	return nil
}

// do runs f under the lock. A trap marks the terminal failed, because the
// emulator's state can't be trusted after it. An allocation failure under
// the memory limit does not: libghostty reports it and stays consistent.
// Close wins over a failure: a closed terminal always returns ErrClosed.
func (t *Terminal) do(f func(in *ghostty.Instance) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inst == nil {
		return ErrClosed
	}
	if t.failed != nil {
		return t.failed
	}
	err := f(t.inst)
	switch {
	case errors.Is(err, ghostty.ErrTrap):
		t.failed = fmt.Errorf("%w: %w", ErrFailed, err)
		return t.failed
	case errors.Is(err, ghostty.OutOfMemory):
		return fmt.Errorf("%w: %w", ErrOutOfMemory, err)
	}
	return err
}

// query runs f under the lock, like do, and returns its value.
func query[T any](t *Terminal, f func(in *ghostty.Instance) (T, error)) (T, error) {
	var v T
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		v, err = f(in)
		return err
	})
	return v, err
}

// Write feeds p to the emulator. It implements io.Writer.
func (t *Terminal) Write(p []byte) (int, error) {
	if err := t.do(func(in *ghostty.Instance) error { return in.VTWrite(p) }); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WriteUntilGround feeds p only up to the byte at which the parser is at
// ground: outside any escape sequence and UTF-8 character. n is the number
// of bytes consumed and ground whether the parser is at ground after
// them. A parser already at ground consumes nothing.
func (t *Terminal) WriteUntilGround(p []byte) (int, bool, error) {
	var n int
	var ground bool
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		n, ground, err = in.VTWriteUntilGround(p)
		return err
	})
	return n, ground, err
}

// Resize changes the size. The primary screen reflows soft-wrapped lines.
func (t *Terminal) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errSize
	}
	return t.do(func(in *ghostty.Instance) error { return in.TerminalResize(cols, rows, 0, 0) })
}

// AltScreen reports whether the alternate screen is active.
func (t *Terminal) AltScreen() (bool, error) {
	return query(t, func(in *ghostty.Instance) (bool, error) {
		v, err := in.GetU32(ghostty.DataActiveScreen)
		return int32(v) == ghostty.ScreenAlternate, err
	})
}

// Mode reports a mode's current value. ansi selects ANSI modes (CSI n h)
// over DEC modes (CSI ? n h).
func (t *Terminal) Mode(mode uint16, ansi bool) (bool, error) {
	return query(t, func(in *ghostty.Instance) (bool, error) {
		return in.GetMode(ghostty.EncodeMode(mode, ansi))
	})
}

// SetMode sets a mode without a VT sequence and without its side effects,
// for example to turn synchronized output off before formatting.
func (t *Terminal) SetMode(mode uint16, ansi, value bool) error {
	return t.do(func(in *ghostty.Instance) error {
		return in.TerminalSetMode(ghostty.EncodeMode(mode, ansi), value)
	})
}

// Modes returns every mode the emulator knows.
func (t *Terminal) Modes() ([]ModeState, error) {
	return query(t, func(in *ghostty.Instance) ([]ModeState, error) {
		out := make([]ModeState, 0, len(ghostty.Modes))
		for _, m := range ghostty.Modes {
			v, err := in.GetMode(m.Mode())
			if err != nil {
				return nil, err
			}
			out = append(out, ModeState{Mode: m.Value, ANSI: m.ANSI, Value: v, Default: m.Default})
		}
		return out, nil
	})
}

// KittyKeyboardFlags returns the active kitty keyboard protocol flags.
func (t *Terminal) KittyKeyboardFlags() (uint8, error) {
	return query(t, func(in *ghostty.Instance) (uint8, error) {
		return in.GetU8(ghostty.DataKittyKeyboardFlags)
	})
}

// CursorStyle returns the cursor's DECSCUSR shape and blinking state.
func (t *Terminal) CursorStyle() (CursorStyle, error) {
	return query(t, func(in *ghostty.Instance) (CursorStyle, error) {
		v, blink, err := in.RenderStateCursor()
		return CursorStyle{Shape: cursorShape(v), Blinking: blink}, err
	})
}

// CursorVisible reports DECTCEM.
func (t *Terminal) CursorVisible() (bool, error) {
	return t.getBool(ghostty.DataCursorVisible)
}

// MouseTracking returns the most inclusive mouse tracking mode that is on.
func (t *Terminal) MouseTracking() (MouseTracking, error) {
	return query(t, func(in *ghostty.Instance) (MouseTracking, error) {
		for _, c := range []struct {
			mode uint16
			mt   MouseTracking
		}{{1003, MouseAny}, {1002, MouseButton}, {1000, MouseNormal}, {9, MouseX10}} {
			on, err := in.GetMode(ghostty.EncodeMode(c.mode, false))
			if err != nil {
				return MouseNone, err
			}
			if on {
				return c.mt, nil
			}
		}
		return MouseNone, nil
	})
}

// Title returns the window title set by OSC 0 or OSC 2.
func (t *Terminal) Title() (string, error) { return t.getString(ghostty.DataTitle) }

// Pwd returns the working directory as the program sent it (OSC 7 is a
// file:// URI).
func (t *Terminal) Pwd() (string, error) { return t.getString(ghostty.DataPwd) }

// AtGround reports whether the parser is outside any escape sequence and
// UTF-8 character.
func (t *Terminal) AtGround() (bool, error) { return t.getBool(ghostty.DataVTGround) }

// Continuation returns the bytes that recreate the unfinished escape
// sequence or UTF-8 character in another terminal. It is empty at ground.
func (t *Terminal) Continuation() ([]byte, error) {
	return query(t, func(in *ghostty.Instance) ([]byte, error) {
		b, err := in.ContinuationAlloc()
		if errors.Is(err, ghostty.InvalidValue) {
			return nil, ErrContinuationUnavailable
		}
		return b, err
	})
}

// Close frees the terminal and its memory. It is idempotent.
func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inst == nil {
		return nil
	}
	err := t.inst.Close()
	t.inst = nil
	return err
}

func (t *Terminal) getBool(d ghostty.TerminalData) (bool, error) {
	return query(t, func(in *ghostty.Instance) (bool, error) { return in.GetBool(d) })
}

func (t *Terminal) getString(d ghostty.TerminalData) (string, error) {
	return query(t, func(in *ghostty.Instance) (string, error) { return in.GetString(d) })
}
