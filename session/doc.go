/*
Package session manages persistent PTY shell sessions that survive SSH
disconnects.

Two diagrams — one per direction — because they are almost independent paths.

Input: user keystrokes → shell stdin

	┌──────┐    SSH     ┌────────┐   WriteInput()    ┌────────┐     ┌────────┐
	│ user │  channel   │ Client │ ──(writer only)─► │  PTY   │ ──► │ shell  │
	│ term │ ─────────► │        │  → s.ptmx.Write   │ master │     │ stdin  │
	└──────┘            └────────┘                   └────────┘     └────────┘

Only the Client currently marked as writer gets to push bytes into the PTY.
Non-writer clients are read-only while they remain non-writers. If the writer
detaches, the most recently attached remaining client is promoted.

Output: shell stdout → user terminal (+ VTE side channel)

	┌────────┐    ┌────────┐     ┌──────────────┐     deliver()     ┌─────────┐  SSH   ┌──────┐
	│ shell  │ ─► │  PTY   │ ──► │   pump()     │ ─── fan-out  ───► │ Client₁ │ ─────► │ user │
	│ stdout │    │ master │     │ ptmx.Read()  │     (N clients)   │ Client₂ │ chan   │ term │
	└────────┘    └────────┘     └──────┬───────┘                   │  ...    │        └──────┘
	                                    │                           └─────────┘
	                                    │ under s.mu
	                                    ▼
	                             ┌─────────────┐   response pipe   ┌──────────┐
	                             │ vte.Write() │ ────────────────► │ drainVTE │  (discarded,
	                             │ updates:    │  (DSR/DA/CPR etc) └──────────┘   safety valve)
	                             │  • screen   │
	                             │  • cursor   │
	                             │  • scrollbk │
	                             │  • alt-scr  │
	                             └─────▲───────┘
	                                   │
	                                   │ snapshot on Attach() / re-attach
	                                   │   • renderVTEScrollback()
	                                   │   • vte.Render()        (visible screen)
	                                   │   • vte.CursorPosition()
	                                   ▼
	                             ┌─────────────┐
	                             │ replay blob │  → prepended to the new client's
	                             │ ESC c +     │    output stream in normal-screen
	                             │ scrollback +│    mode, so its terminal is
	                             │ screen +    │    restored across reconnects
	                             │ cursor pos  │
	                             └─────────────┘

Three things worth calling out:

 1. The VTE is on a side branch of the output pump, not in series. Live clients
    get the raw PTY bytes unmodified. The VTE is fed on every pump read and
    is continuously drained by drainVTE, but its rendered screen, cursor
    position, and scrollback are only consulted on Attach() to synthesize
    replay state. If nobody ever re-attaches, the VTE is mostly just
    absorbing output to preserve future replay state.

 2. The normal-screen replay path uses the blob shown above: terminal reset,
    rendered scrollback, visible screen, and cursor position. Alternate-screen
    sessions take a different path. When vte.IsAltScreen() is true, Attach()
    sends enter-alt-screen + clear to the new client, writes Ctrl-L directly to
    the PTY, and relies on the running application to redraw.

 3. Terminal queries get answered by the writer client's real terminal, not by
    the VTE. When an app writes ESC[6n, those bytes flow to the VTE and to every
    attached client's SSH channel. A real terminal emulator can respond on its
    input stream, but only the writer client's response can pass WriteInput() and
    reach the PTY; non-writer responses are rejected as read-only. The VTE's own
    response goes into drainVTE and is thrown away. With no client attached,
    TUI queries go unanswered. The drain exists to keep vte.Write from
    blocking on a full response pipe.
*/
package session
