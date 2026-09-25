// Package overlay shows a Bubble Tea program over a container session in
// the user's terminal, in any terminal emulator.
//
// A shadow terminal emulator (package vt) sees every byte the container
// writes but never sits between the container and the screen. Showing a
// prompt is a detach inside this process: output stops being forwarded at
// a byte where the shadow's parser is at ground (T1), stdin moves to the
// prompt at the terminal's reply to a barrier query (T2), and afterwards
// the screen is restored, by replaying the output logged meanwhile or from
// the shadow, while output and input switch back atomically (T3). See
// docs/superpowers/specs/2026-09-24-ghostty-shadow-terminal-design.md.
package overlay

import "errors"

var (
	// ErrUnavailable means prompts can't be shown in this session: the
	// shadow terminal is off or failed. Output still passes through.
	ErrUnavailable = errors.New("overlay: unavailable")
	// ErrBarrierTimeout means the terminal didn't answer the barrier query
	// in time. Nothing was drawn.
	ErrBarrierTimeout = errors.New("overlay: terminal did not answer the barrier query")
	// ErrClosed means the session's input or output has ended.
	ErrClosed = errors.New("overlay: session ended")
)
