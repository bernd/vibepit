package ghostty

import (
	"errors"
	"fmt"
)

// ErrTrap matches every *TrapError. After a trap the instance's state can't
// be trusted.
var ErrTrap = errors.New("ghostty: wasm trap")

// TrapError is a WebAssembly trap, which in translated code is a panic
// (including one from the write_pty callback), or a pointer from the module
// that points outside its memory.
type TrapError struct {
	Func string
	Err  error
}

func (e *TrapError) Error() string        { return fmt.Sprintf("%s: wasm trap: %v", e.Func, e.Err) }
func (e *TrapError) Is(target error) bool { return target == ErrTrap }
func (e *TrapError) Unwrap() error        { return e.Err }

// CallError is a C function that returned a GhosttyResult other than
// SUCCESS. errors.Is matches the Result.
type CallError struct {
	Func   string
	Result Result
}

// Error prints the result via Result.String, not the error-interface %s
// dispatch (which would call Result.Error and double the "ghostty: "
// prefix into "ghostty: X returned ghostty: INVALID_VALUE").
func (e *CallError) Error() string {
	return fmt.Sprintf("ghostty: %s returned %s", e.Func, e.Result.String())
}
func (e *CallError) Unwrap() error { return e.Result }

var errOutOfBounds = errors.New("memory access out of bounds")
