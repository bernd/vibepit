# Vibepit desktop experiment

A **throwaway prototype** using Wails 3.1, Svelte 5, TypeScript, Vite, and
wterm 0.5.10 with `@wterm/ghostty`. It attaches to an existing `vibepit up` sandbox and provides:

- Independent project windows, a tray menu, and native approval/completion notifications.
  Closing a window hides it and keeps its terminal and Claude process connected.
- A Ghostty terminal connected directly to an SSH PTY, including input and resize.
- A Claude conversation using bidirectional JSON over a separate, non-PTY SSH
  channel, with streamed text, tool activity, allow/deny tool requests, and interrupt.
- Model and effort selectors using Claude’s advertised model list and acknowledged
  control requests. Switch between turns without losing the conversation.
- A **Claude debug** tab with timestamped stdin/stdout frames, stderr, and write
  failures. Capture runs even when the tab is closed and includes initialization,
  permission/control messages, and malformed output. Filter by stream, expand a
  record, and switch between pretty JSON and raw text. Input records show write
  attempts; a failed write gets a separate transport-error record.
- A shared network panel using Vibepit's existing mTLS event stream and control
  API. Allow a held HTTP(S) destination for the running session, or reject it.

The terminal continues to receive output while permission requests are visible.
This app does not use the CLI's host-side shadow terminal/overlay path. The
existing server-side terminal session implementation is unchanged.

## Try it without a sandbox

From the repository root:

```sh
cd experiments/desktop
make demo
```

Open <http://127.0.0.1:9245>, click **Connect**, then open the **Claude** tab and
send a message. Approve the simulated tool request to see a streamed reply and
a separate network request. Try allowing/rejecting the network request,
interrupting a reply, and switching back to the terminal.

Demo mode uses the real Svelte frontend, wterm DOM renderer and Ghostty core, Wails bindings,
Go service, and event bridge, with simulated sandbox and agent data. It makes no
model calls and executes no shell commands. The terminal only echoes input.
The browser/server build deliberately refuses live mode.

## Try the native app with a real sandbox

Requirements: this repository's Go version, a current Node.js/npm installation,
Docker or Podman access, and the Wails platform runtime libraries. The prototype
uses `purego` builds; a separate Wails CLI installation is unnecessary.

On Linux, Wails' default purego backend requires GTK 4 and WebKitGTK 6. On
Debian/Ubuntu the runtime packages are `libgtk-4-1` and `libwebkitgtk-6.0-4`.
For a GTK 3 host, use `CGO_ENABLED=0 go run -tags 'purego gtk3' .` after building
the frontend; that needs GTK 3 and WebKit2GTK 4.1 instead. See the
[Wails installation guide](https://wails.io/) for platform setup.

1. In the project you want to work on, run `vibepit up`.
2. Ensure Claude Code is installed and authenticated **inside that sandbox**.
   Use `vibepit connect` for installation/login if necessary.
3. From this checkout, run:

   ```sh
   cd experiments/desktop
   make run
   ```

4. Select the running sandbox and click **Connect**. The terminal opens first;
   open **Claude**, choose whether to enable **YOLO · skip Claude approvals**,
   then click **Start Claude** to load model choices. Sending a first message
   also starts Claude with the selected mode.

The app checks the SSH host key against Vibepit's stored key and loads the same
SSH/mTLS credentials as the CLI. Credentials stay in Go, outside the web UI.
It never starts Claude on the host. Existing Claude settings and hooks apply;
the launcher uses the default permission mode unless you select YOLO at startup.

To show the simulated data in a native window, build the frontend once and run
`CGO_ENABLED=0 go run -tags purego . --demo`.

## Windows, tray, and notifications

Use **New window** for another independent sandbox connection and conversation.
Each window owns its terminal, Claude process, permission state, and debug stream.
Two windows may attach to the same sandbox; its network policy remains shared.

In the native app, **Hide window** and the window's close button hide its webview,
keeping the running agent and transcript intact. The tray lists each workspace's
connection/agent status and pending approvals. Choose a workspace to reopen it,
**Show all windows**, or **New window**. **Quit Vibepit Lab** disconnects every
workspace and closes its logs. Closing the last window does not quit the app.
**File → Quit Vibepit Lab** and **Ctrl+Shift+Q** (**Cmd+Q** on macOS) also quit.
On Linux without a registered tray host, close/hide minimizes the window instead,
so it remains accessible from the taskbar. Left-clicking the Linux tray icon
restores the windows; its context menu provides the other actions.

Choose **Enable notifications** to request OS permission. Background windows can
notify for network/tool approvals, finished turns, errors, and Claude exits.
Clicking opens the relevant window for review; it never approves a request.
On Linux, the app carries the notification daemon's activation token to GTK before
presenting that window, so GNOME/Wayland can recognise the click as user input.
This uses a small D-Bus adapter because Wails 3.1 drops that token. Other platforms
continue using Wails' notification service. Desktops that omit the token may
still require a second activation; the app logs that case.
Notifications are suppressed while that window is focused by default. Enable
**Notify even when focused** beside the notification button to test OS alerts
without switching apps. This applies to future notifications for that window and
lasts until the app quits; it does not grant OS permission or replay old notices.
Notifications are withdrawn when
resolved, cancelled, expired, disconnected, or opened. Failed or unavailable
native notification services leave the terminal and agent usable.

Native notifications depend on the desktop environment: Linux needs a session
D-Bus notification service and tray support (some GNOME desktops need an
AppIndicator extension). macOS requires an appropriately bundled/signed app for
notifications; plain `go run` may not support them. Wails 3.1's Windows toast
removal methods are currently stubs, so a resolved toast may remain in Action
Center; its click handler is invalidated.

Browser preview opens separate browser windows and omits native tray and
notification controls. Reloading a connected page shows **Disconnect and reset**
explicitly: this prototype cannot reconstruct its in-memory transcript after a
reload, and it leaves the connection running until you choose to disconnect.
Hidden native windows retain their webviews and do not need this recovery.

## Native menus and clipboard

The native app has **File**, **Edit**, **View**, and **Window** menus. File opens,
hides, or quits windows. View switches between Terminal, Claude, and Claude debug
without starting Claude. Window minimizes the current window, restores all
windows, or selects a workspace by name.

Edit provides Undo, Redo, Cut, Copy, Paste, and Select All for the active view.
Terminal copy uses Ghostty's selection, and paste goes through Ghostty's paste
API so bracketed-paste mode is preserved. Cut never deletes terminal output.
Composer and other text fields update normally; selected conversation/debug text
can also be copied. Clipboard failures appear in the app. A delayed paste is
cancelled if its destination changes or disconnects.

On Linux/Windows, use **Ctrl+Shift+C/V** to copy/paste, **Ctrl+Shift+X/A** to
cut/select all, and **Ctrl+Shift+Z/Y** to undo/redo in text fields. Ordinary
Ctrl+C, Ctrl+A, Ctrl+Z and Backspace remain available to terminal applications.
Native macOS menus use Command equivalents, with Cmd+Shift+Z for Redo.
**Ctrl+Shift+N/W/Q** opens/hides/quits windows, and **Ctrl+Shift+1/2/3** selects
the three views; on macOS these use Command without Shift.

The native build reads/writes the system text clipboard through Wails. Browser
preview uses the browser clipboard and its permission rules; it has no native
menu bar. Clipboard content is accessed only for an explicit editing action.

## Terminal tools and shell reports

**Find** searches the terminal's retained output (up to 5,000 history lines),
with match counts, case sensitivity, and next/previous controls. Open it with
**Ctrl+Shift+F** or **Cmd+F** on macOS. Enter moves forward, Shift+Enter moves
backward, and Escape returns focus to the terminal. Search accepts up to 1,024
UTF-16 code units; ordinary Ctrl+F remains available to shell applications.

Select output, then choose **Send selection to Claude** to append it as a fenced
text block to the existing composer draft. The app opens and focuses Claude's
composer; it does not start Claude or submit the draft. Terminal painting pauses
while another tab is active, while parsing, history and shell reports continue.

The terminal toolbar shows the window title set by applications such as Vim and
Claude via OSC 0 or OSC 2. It updates even while the terminal tab is hidden,
clears when the application sends an empty title, and resets on reconnect. Long
titles are truncated visually; hover to see the full title.

The toolbar also shows shell-reported working directory and command state:
ready, running, completed, or the last nonzero exit status. These come from OSC 7
and OSC 133, so shells without integration show the metadata as unavailable.
Directory reports are display-only; they do not change Claude's working directory.

Desktop SSH sessions request `VIBEPIT_SHELL_INTEGRATION=1`. The default image's
Bash configuration uses that opt-in to load `/etc/vibepit/shell-integration.bash`,
preserving existing prompt hooks and DEBUG traps. Rebuild the sandbox image and
start a new Bash session to enable it. Reattaching an existing persistent shell
does not change its environment or install hooks. To enable reports manually in
an existing Bash shell using the updated image:

```bash
export VIBEPIT_SHELL_INTEGRATION=1
source /etc/vibepit/shell-integration.bash
```

Custom shell configurations must source the integration themselves, or emit
compatible OSC reports. The browser demo supplies simulated reports.

## Deliberate limits

- One attached sandbox, terminal, and Claude process per window. Sandbox
  creation, image management, multi-agent tabs, and packaging are out of scope.
- Only the Claude adapter exists. Its wire protocol is isolated in
  `backend/agent.go`; another agent needs its own adapter.
- Network decisions apply to the whole sandbox, not an attributed tool/process.
  Allows last for the session and are not written to project configuration.
  Reject uses the existing session rejection behavior, not a persistent deny rule.
- The network stream reconnects and reloads pending holds. Only held HTTP(S)
  requests get approval controls. DNS blocks are visible in recent activity.
- Terminal persistence remains owned by `vibed`. Claude runs as an ordinary
  SSH exec process and is not managed as a persistent terminal session. There is
  no automatic agent reattachment/resume or guaranteed descendant-process cleanup.
  Disconnect/reconnect to restart an exited agent; the transcript in this UI is
  cleared. Claude's own transcript files remain subject to its normal behavior.
- The Claude control protocol is based on
  [Anthropic's SDK implementation](https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/_internal/query.py),
  including initialization, permission responses, and interruption. This is a
  small direct adapter, not the complete SDK. Specialized question forms, custom
  hooks over the control channel, SDK MCP servers, and subagent conversation
  trees are not implemented. Unknown control requests receive an explicit error.
- Assistant replies render sanitized Markdown. Tool calls have descriptive,
  expandable rows with structured output and neutral completion states.
  Diagnostics are bounded, but the conversation itself is kept in memory.
- The debug tab keeps the newest 1,000 records, capped at approximately 16 MiB
  of raw text, and reports discarded records. It retains the log after disconnect
  for inspection; reconnecting or **Clear view** resets the displayed records.
  Complete recordings are saved to disk independently of this display limit.
  Demo mode includes simulated input and output frames in the same view.
- Terminal output is base64-encoded across the Wails bridge to preserve arbitrary
  bytes. Binary input (including legacy mouse reports) also uses base64; text
  input uses the string RPC. This spike has no high-volume backpressure benchmark.
  Vite emits the Ghostty WASM as a hashed local asset embedded alongside the UI;
  the app does not fetch it from a CDN.

## Claude startup mode

Before starting Claude, enable **YOLO · skip Claude approvals** to launch with
`--dangerously-skip-permissions`, the same flag used by Vibepit's `yoloclaude`
wrapper. Leave it unchecked for the usual tool permission prompts. The mode is
fixed for that process; disconnect and reconnect to choose a different mode.
Opening the tab alone no longer launches Claude, leaving time to choose.

The UI shows the permission mode Claude reports through initialization. If an
older CLI omits that field, it labels the requested mode as unconfirmed until a
`system/init` report arrives. Startup commands and reported mode are recorded in
the debug tab and disk log. YOLO does not change Vibepit's network filtering or
network approval panel. The demo exercises both startup modes.

## Claude model and effort

The **Model** and **Effort** controls sit above the conversation. **Start Claude**
performs the initialization handshake; it does not send a model task. Model names
come from the installed Claude CLI, rather than a hardcoded version list. The
small text below the controls also shows the last main-thread model reported in
`system/init`, an assistant message, or a streamed message start.

Switches are session-only and apply to the next turn. The controls and composer
wait for the matching success response before showing an accepted change. Model
switches send `set_model`; effort switches send `apply_flag_settings` with
`settings.effortLevel`, following the official
[TypeScript Agent SDK](https://code.claude.com/docs/en/agent-sdk/typescript).
Both directions are captured in **Claude debug** and on disk.

Effort initially says **Inherited · not reported**: initialization does not
report the effective effort. After a successful switch, the selector shows the
requested override, not a promise about API behavior. Claude may apply model or
organization limits. Clearing an override sends `null` to restore the model's
default. Advertised effort capabilities determine the choices; older CLIs that
omit them get low/medium/high choices and can explicitly reject unsupported
requests. A rejected switch keeps the prior value. A missing acknowledgement
closes the process after 15 seconds to avoid continuing with unknown settings;
reconnect to restart. An old CLI may support model changes but reject runtime
effort changes; its error is shown without silently restarting the conversation.

## Stream-driven conversation UI

- The usage strip reports the advertised subscription/authentication source, usage
  windows, reset times, and overage state. Expand it for details. Missing fields
  stay unknown, and reset timestamps do not silently zero the last reported usage.
  API credential reports take precedence over the subscription label. Dollar
  estimates remain in the raw debug log; they are not presented as charges.
- Progress distinguishes requesting, thinking (elapsed time and reported token
  estimates), tool activity, waiting for approval, and writing a reply. Aborted
  turns and withdrawn permission requests settle pending indicators.
- Tool rows use descriptions and file paths, with separate stdout/stderr or file
  content. Large outputs scroll inside collapsed details. **Completed** means a
  tool returned, not that its tests or requested task succeeded.
- Markdown supports headings, lists, code, tables, and HTTP(S) links opened in the
  system browser. Output is sanitized with DOMPurify; active HTML, images, forms,
  and unsafe URL schemes are excluded.
- Each turn has an expandable duration/tool-count/token summary. Context is an
  estimate from the latest main-thread request's input, cache and output tokens;
  context capacity appears when Claude reports it. Accumulated turn usage is
  never used as the context-window numerator.
- Type `/` or open **Commands** to search advertised names/descriptions. Use arrow
  keys and Enter/Tab to insert, Escape to dismiss, then add arguments and Send.
  Browsing preserves an existing draft. Commands are submitted as ordinary user
  prompts, as described in the official
  [Agent SDK command documentation](https://code.claude.com/docs/en/agent-sdk/skills#dispatch-commands-by-name).
  Availability is controlled by the installed CLI; terminal-only commands are
  not added to the menu. Demo commands produce simulated replies.

The UI was also replayed against a local live recording. Private recordings are
not bundled as fixtures. Synthetic state tests cover missing metadata, duplicate
messages, tool lifecycle, cancellations, abort reasons, and context accounting.

## Inspect Claude stream recordings

Every Claude process gets a separate JSONL file in `logs/claude/` relative to the
app's working directory. With the Make targets, that is
`experiments/desktop/logs/claude/`, which is gitignored. The absolute path appears
in **Claude debug** and is printed to the app's console. Filenames distinguish
`claude-live-...jsonl` from `claude-demo-...jsonl` so simulated traffic cannot be
mistaken for evidence of real Claude behavior.

To choose a different location after building the frontend:

```sh
CGO_ENABLED=0 go run -tags purego . --stream-log-dir /absolute/path/to/recordings
```

The first line has `kind: "session"`, a schema version, connection ID, sandbox
project, start time, and demo flag. Subsequent `kind: "stream"` lines contain
`sequence`, `time`, `stream` (`stdin`, `stdout`, `stderr`, or `transport`), and
`raw`. Valid stdin/stdout JSON also appears in `message`, making event types and
fields directly queryable. Malformed stdout and stderr remain intact in `raw`.
Stdout records contain the received line without its NDJSON delimiter; stdin
records include the newline submitted to the pipe. Outgoing records describe
write attempts, with separate transport records if writing fails.

For example, from this directory, replace the filename with the one shown in
the app:

```sh
tail -f logs/claude/claude-live-EXAMPLE.jsonl
jq -r 'select(.kind == "stream") | [.sequence, .stream, .message.type, (.message.subtype // .message.event.type // .message.request.subtype // "")] | @tsv' logs/claude/claude-live-EXAMPLE.jsonl
jq 'select(.stream == "stdout" and .message.type == "assistant") | .message' logs/claude/claude-live-EXAMPLE.jsonl
```

Each line is written immediately for inspection during a run; the file is synced
and closed when the agent exits or the app disconnects. Files are created with
owner-only permissions and contain the full prompts, tool inputs/results, and
other stream contents. There is no automatic truncation or deletion: clearing
the UI leaves the recording intact. Creation failures prevent agent startup;
later disk errors appear in the app and console without stopping the agent.

These recordings are the evidence to inspect when deciding which Claude message
types, tool states, and control events deserve dedicated UI. Demo recordings only
verify the recording pipeline; use a live session to evaluate real message shapes.

## Development and validation

This is a nested Go module with a local `replace` to the repository root. It
changes no existing CLI implementation or root dependency files. Root `make test`
does not discover this nested module; run its checks separately:

```sh
make check
go test -race ./backend
cd frontend && npm test
```

After frontend edits, rerun `make demo` or `make run` to rebuild the embedded
assets. `npm run dev` alone serves the UI but does not provide the Wails bridge.

Validated in the development container: frontend type checking and build,
Svelte analysis, backend protocol/permission/byte tests with the race detector,
Linux native compilation, and a two-window browser smoke test using Wails' demo
server. The window test covers concurrent conversations, independent disconnect,
and explicit reload recovery. Registry tests cover cross-window token rejection,
notification deduplication/expiry, and concurrent shutdown.
The repository's `make test` and `make test-integration` also passed.

The wterm migration was checked in the built browser demo: local Ghostty WASM
loading, UTF-8 split across output events, input and resize RPCs, binary X10 mouse
reports, native-menu selection and bracketed paste, paste after mouse selection
and while viewing history, output while the tab is hidden, reconnect isolation,
and disconnect during WASM loading. Frontend tests also cover paste encoding;
backend tests cover binary input and workspace ownership.

Terminal tools were checked in the built browser demo for scrollback search,
case sensitivity and navigation, selection-to-draft without submission, paused
painting with continuing shell reports, and status/directory display. Bash tests
exercise actual interactive prompt expansion through a PTY, command exit status,
directory encoding, idempotent sourcing, and preservation of existing hooks.

**Still requires host verification:** opening the native WebKit window, attaching
to real containers, and exercising the installed Claude version. Also verify
tray hide/reopen during an active turn, notification authorization and clicks,
and Quit with multiple connected windows. The development
container has neither the required WebKit runtime nor a Docker/Podman socket.
In particular, verify typing/paste, Unicode, terminal resize, a live held network
request, multiple Claude turns, tool denial, and interruption before judging
whether to keep any of this code.

## Why this frontend

[wterm](https://github.com/vercel-labs/wterm) supplies the DOM renderer and input
handling through `@wterm/dom`; `@wterm/ghostty` supplies the terminal engine.
The app explicitly loads the Ghostty core instead of wterm's default engine.
Each connection owns and disposes its own core. Native edit commands use wterm's
selection API and preserve Ghostty's bracketed-paste mode. Native WebKit behavior
remains to be checked. It does not reuse Vibepit's Go-translated WASM module.

Svelte handles the stateful transcript and permission queues; TypeScript checks
the small handwritten Wails boundary. Vite builds static embedded assets.
SvelteKit, a router, and a global state library are unnecessary for this single
desktop surface. Plain TypeScript would also work, but would move the message
and permission-state bookkeeping into manual DOM code.
