/*
Package ward wraps a child process in a pseudo-terminal and reserves the
last terminal row for a notification bar.

Two data paths (input and output) plus a status channel feed into a single
event loop that owns the bar. The stdin path includes a two-state machine
(normal/command) that intercepts the hotkey (Ctrl+], 0x1D) when OnKey is
configured and stdin is a TTY.

Input: user keystrokes → child stdin (+ window-size report rewriting)

	┌──────┐            ┌──────────┐   stdin.Read()   ┌─────────────────┐     ┌────────┐     ┌───────┐
	│ user │  os.Stdin  │          │  ──────────────► │ winsizeRewriter │ ──► │  PTY   │ ──► │ child │
	│ term │ ─────────► │ Wrapper  │                  │ .Write()        │     │ master │     │ stdin │
	└──────┘            │          │                  └─────────────────┘     └────────┘     └───────┘
	                    └──────────┘                   rewrites CSI 4/8/9/48 … t
	                                                   to the N-1 row PTY height

Output: child stdout → user terminal (+ escape sequence scanning)

	┌───────┐    ┌────────┐     ┌──────────────┐     ┌───────────┐     ┌──────┐
	│ child │ ─► │  PTY   │ ──► │  output      │ ──► │ escScanner│ ──► │ user │
	│stdout │    │ master │     │  goroutine   │     │ .Scan()   │     │ term │
	└───────┘    └────────┘     │  ptmx.Read() │     └─────┬─────┘     └──────┘
	                            └──────────────┘           │
	                                                       │ detects:
	                                                       │  • ESC c (RIS)
	                                                       │  • CSI r (no params)
	                                                       │  • CSI J / 2J / 3J
	                                                       ▼
	                                                 ┌─────────────────┐
	                                                 │ re-emit scroll  │
	                                                 │ region + repaint│
	                                                 │ bar if needed   │
	                                                 └─────────────────┘

Notifications: status channel feeds the event loop

	┌───────────┐
	│  Status   │  barEventStatus / barEventAlert
	│  channel  │ ─────────────────┐
	│ (Go chan) │                  │
	└───────────┘                  ▼
	                          ┌────────────────────────────────────────────────┐
	                          │                event loop                      │
	                          │                                                │
	                          │  • barEventStatus → update lastStatus,         │
	                          │    show if no active alert                     │
	                          │  • barEventAlert  → show immediately or queue  │
	                          │    (max 64), start dismiss timer               │
	                          │  • barEventDismiss → pop next alert or fall    │
	                          │    back to lastStatus or clear                 │
	                          │                                                │
	                          │  sole owner of bar content; writes to stdout   │
	                          │  under outputMu                                │
	                          └──────────────────────┬─────────────────────────┘
	                                                 │
	                                                 ▼
	                          ┌────────────────────────────────────────────────┐
	                          │              terminal last row                 │
	                          │  ╱╱╱ VIBEPIT  message ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱    │
	                          └────────────────────────────────────────────────┘

Command mode lifecycle (stdin goroutine ↔ event loop):

	stdin goroutine                    event loop
	───────────────                    ──────────
	detect 0x1D
	  → barEventEnterCommand ────────► commandGen++
	  ← commandResponse ◄────────────  stop dismiss timer
	                                   start idle timer
	                                   render command bar

	(waiting for key)

	matched key pressed
	  → barEventBeginAction ─────────► stop idle timer
	  ← ack (true) ◄────────────────── enter pendingAction

	call OnKey(ctx, key, target)

	  → barEventAction ──────────────► flash result
	                                   dismiss alert
	                                   process queue

Terminal protection via DECSTBM scroll region:

	row 1   ┌────────────────────────┐
	        │                        │  ◄── scroll region: rows 1..(height-1)
	        │   child output area    │      child's PTY size = height-1 rows
	        │                        │      normal scrolling confined here
	row N-1 │                        │
	        ├────────────────────────┤
	row N   │  ward notification bar │  ◄── protected: outside scroll region
	        └────────────────────────┘

Five things worth calling out:

 1. The scroll region is the primary protection mechanism. Ward sets
    DECSTBM to rows 1..N-1 so normal output and scrolling cannot reach
    row N. The child's PTY is sized to N-1 rows, so it believes the
    terminal is one row shorter than reality. Ward re-applies the scroll
    region after any detected reset (ESC c, parameterless CSI r).

 2. The escScanner is intentionally minimal — not a terminal emulator.
    It recognizes just enough ECMA-48/DEC grammar to detect the three
    sequence classes that can destroy the bar (reset, margin clear,
    screen erase). It also tracks whether the byte stream is mid-sequence
    so ward never injects its own escapes inside the child's incomplete
    sequences.

 3. The bar is not repainted on every PTY read. Doing so would leak bar
    escape sequences into terminal scrollback. Repainting occurs only on
    three occasions: when the escScanner detects a scroll/erase reset,
    when the event loop changes bar content, and when SIGWINCH fires.

 4. The PTY size alone does not hide row N. Applications such as tmux
    query the terminal directly (CSI 18 t / CSI 14 t) and trust the reply
    over TIOCGWINSZ, and terminals with in-band resize (mode 2048) push
    CSI 48 … t unsolicited on every resize. The winsizeRewriter on the
    stdin path rewrites those XTWINOPS reports to the N-1 row height so
    the child's two views of the terminal agree. Replies are rewritten on
    input rather than queries suppressed on output because the in-band
    notifications have no query to suppress. A report prefix split across
    reads is held for at most 50 ms; a lone ESC is never held, so the Esc
    key stays responsive.

 5. In command mode a complete escape sequence (arrow or function key,
    mouse or focus event, size report, OSC/DCS reply) is forwarded intact
    and does not cancel command mode: command keys are single bytes, so a
    sequence is never meant for the bar. A bare Esc, Alt+key, a sequence
    cut at the read boundary and a bracketed paste still cancel; all but
    the Esc are forwarded with their ESC intact.
*/
package ward
