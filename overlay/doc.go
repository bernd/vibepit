// Package overlay shows a Bubble Tea program over a session, a container
// attach or an SSH shell, in the user's terminal, in any terminal emulator.
//
// A shadow terminal emulator (package vt) sees every byte the session
// writes but never sits between the session and the screen. Before the
// first byte, its cursor moves to the real terminal's, which the terminal
// reports to a DSR 6n query, so both put the output on the same rows.
//
// Showing a prompt is a detach inside this process, in three steps:
//
//   - The cut: output stops being forwarded at a byte where the shadow's
//     parser is at ground, outside any escape sequence, so the real
//     terminal isn't left inside one. If no such byte comes within a time
//     and byte limit, a forced cut writes CAN to abort the sequence. From
//     the cut on, output goes to the shadow and a log, and the shadow
//     answers the app's terminal queries. Input still goes to the session,
//     because stdin may still carry keys and replies meant for the app.
//     The cut ends by writing the barrier query, DSR 5n.
//   - The handoff: the terminal's reply to the barrier query, CSI 0n,
//     moves input to the prompt at that byte. Terminals answer in order,
//     so every reply the app was owed has reached it by then. Only now is
//     the prompt drawn. A terminal that didn't answer twice in a row gets
//     no more barrier queries; input then moves after a pause in typing.
//   - The leave: after the prompt exits, the screen is restored, and
//     output and input switch back to the session at once. The leave
//     replays the logged output when that reproduces the screen exactly
//     (raw replay), else rebuilds the screen from the shadow (snapshot),
//     else resets the terminal and makes the app repaint.
//
// The prompt's state is what the prompt may change on the real terminal
// and the leave restores: the prompt's screen, the kitty keyboard flags,
// the modes IRM, LNM, DECSCNM, DECOM, DECAWM, DECTCEM and synchronized
// output, the scroll region and the pen (SGR attributes, hyperlink and
// charsets). Everything else keeps its value from the cut;
// NewFilter drops prompt output that would change it.
//
// The design and its rationale are in
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
