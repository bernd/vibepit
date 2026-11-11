# Shadow Terminal, Step 2: `overlay` Package and `run --prompt` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the proxy blocks a connection during `vibepit run --prompt`, show an allow/deny prompt over the running agent in any terminal, and restore the agent's screen exactly afterwards, using the `vt` shadow terminal from step 1.

**Architecture:** A new `overlay` package sits between the hijacked container stream and the local terminal. One goroutine feeds every container byte to a shadow `vt.Terminal` and, while attached, to stdout. `InputMux` is the only reader of stdin. `Terminal.Show` detaches output at a byte where the shadow's parser is at ground (T1), waits for the terminal's reply to a DSR 5n barrier query before handing stdin to the prompt (T2), runs a Bubble Tea program through a whitelist output filter, and on T3 restores the screen by raw replay of the logged output or by a snapshot from the shadow, under both locks. `container.runTTYSession` routes its I/O through an `overlay.Terminal`; `cmd` ports the approve screen and block poller from the kitty proof of concept and wires them to `Show`.

**Tech Stack:** Go 1.27, `vt` (libghostty-vt translated by wasm2go, step 1), Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.10), `github.com/charmbracelet/x/ansi` v0.11.8 (the filter's parser), `github.com/charmbracelet/colorprofile`, urfave/cli v3, testify.

**Spec:** `docs/superpowers/specs/2026-09-24-ghostty-shadow-terminal-design.md` (sections "Package `overlay` (phase 1)", "Error Handling", "Testing → Other packages"). The `InputMux` background and `approveScreen` come from `docs/superpowers/specs/2026-09-06-inline-overlay-prompt-design.md` (untracked; read it, don't commit it).

**Scope:** Rollout step 2 of the spec. The manual terminal matrix (step 3), `connect --prompt` (step 4) and the `session/` swap (step 5) are out of scope. The kitty proof of concept lives on branch `add/kitty-prompt`, not on this branch; port only the pieces listed in the File Structure. Never port `kitty/`, `cmd/approve.go`, `kittyPrompter`, `approveCmdline`, `SessionInfo.CredDir` or `connect --prompt`, so the spec's "remove the kitty PoC" needs no deletions here.

## Global Constraints

- Builds stay `CGO_ENABLED=0` (the Makefile exports it). The race detector runs with `CGO_ENABLED=1 go test -race`.
- Nested vibepit sandbox: no Docker/Podman. Validate with `make test`, `make test-integration` and `CGO_ENABLED=1 go test -race ./overlay/... ./container/... ./cmd/... ./proxy/...`. Never run `vibepit run` here.
- Leave the untracked files under `docs/superpowers/` alone: never `git add` them, never delete them. Stage files by explicit path, never `git add -A` or `git add .`.
- The formatter patches in `vt/internal/ghostty/patches/` stay local. Upstream PRs are deferred. Nothing in this plan touches `vt/`.
- In non-test code, only `overlay` imports `vt` in this step (the `cmd` render test uses `vt` as a stand-in screen). `overlay` imports nothing from `cmd`, `container` or `tui`.
- Passthrough is byte-for-byte while no prompt shows. The shadow never sits between the container and the screen.
- The output pump never blocks on the prompt and never applies backpressure to the container. The raw log cap is 4 MiB; past it the log is dropped and the leave uses a snapshot.
- Budgets: detach at ground within 64 KiB of forwarded bytes or 250 ms, else a forced cut that writes `CAN` (0x18). Barrier reply within 500 ms. After 2 consecutive barrier timeouts, T2 switches input after 100 ms of input silence. A late barrier reply is stripped for 10 s. Prompt input buffer 64 KiB.
- Shadow on the host: the local terminal's size, 1000 scrollback lines, 64 KiB continuation limit, `write_pty` installed. The overlay never formats `vt.RegionScrollback`. When a `vt.RegionScreen` Format fails with `vt.ErrOutOfMemory`, the shadow stays usable (not marked failed) and the leave falls back to reset. ("Skip the history, keep the screen" is the phase-2 rule for `vibed`; the overlay has no history to skip.)
- Turn synchronized output off with `SetMode(2026, false, false)` on the shadow before every Format that feeds a snapshot.
- The snapshot leave keeps the order of `snapshotLeave` in `vt/internal/ghostty/restore_test.go`: pop and leave the prompt's screen, match the screen, reconcile every mode, pen reset, clear, formatter output. It writes the report-triggering modes (1004, 2031, 2033, 2048) only when the shadow's value differs from the real terminal's known value (the cut value).
- Set D (the only state enter may change): the prompt's screen (`CSI ?1047h`, never `1049`), kitty keyboard (push `CSI >0u`, pop `CSI <u`), modes IRM 4, LNM 20, DECSCNM 5, DECOM 6, DECAWM 7, DECTCEM 25, 2026, the scroll region, the pen (SGR, OSC 8, charsets), the cursor position.
- `--prompt` is a plain `cli.BoolFlag` on `run`, default `false`.
- Go style: `gofmt`, comments explain why, `any` not `interface{}`, table-driven tests with subtests, testify `assert`/`require`. Never call `require`/`t.Fatal` from a goroutine other than the test's.

## Findings From Plan Research (deviations from the spec)

Probed on this branch while writing the plan. Every item has a test below.

1. **The filter's allowlist needs Bubble Tea's renderer sequences.** Bubble Tea v2 renders through ultraviolet, which picks sequences by `TERM` (`xtermCaps` in `ultraviolet/terminal_renderer.go`): REP, HPA, CHT, CBT, SU, SD, plus RI from its hard-scroll path. The spec's list drops them and would corrupt the prompt. The filter also passes REP (`CSI b`), HPA (`` CSI ` ``), HPR (`CSI a`), VPR (`CSI e`), CHT (`CSI I`), CBT (`CSI Z`), SU (`CSI S`), SD (`CSI T`), and RI, IND, NEL (`ESC M`, `ESC D`, `ESC E`). They only move the cursor or edit content on the prompt's screen, which set D already covers. `TestApproveScreen_RendersThroughPromptFilter` (Task 12) checks four `TERM` values.
2. **Enter clears the screen in both cases.** Bubble Tea's renderer assumes a clear screen. The spec writes `CSI 2J` only after `?1047h`; on an alternate-screen cut the app's content would show around the prompt. The alt content is overwritten anyway and is never restored by replay.
3. **The snapshot formats without the Modes extra and enters the alternate screen itself.** The formatter's Modes extra re-emits every non-default mode, so it would enable 1004 and 2048 a second time after the reconciliation skipped them, making the terminal send reports. It would also enter the alternate screen only after `CSI 2J`, which clears the primary screen on the real terminal. So "match the screen" also enters the alternate screen (with the shadow's mode, 1049 first) before the clear, after restoring the cut's modes, pen and cursor (`rawLeave(c, entered{}, nil)`): `1049` saves the cursor and pen, and libghostty, like xterm, keeps one cursor for both screens, so it would otherwise save the prompt's. The order of `snapshotLeave` is unchanged. Test: `TestSnapshotLeaveRestoresChangesWhileDetached/app_enters_the_alternate_screen`.
4. **The snapshot reset also resets modifyOtherKeys (`CSI >4;0m`)**, for the same reason `CSI =0;1u` is there: the Keyboard extra emits it only when set.
5. **The raw leave writes the pen reset (without its kitty part) before `cut.extras`.** The formatter emits the scroll region, SGR, hyperlink and charsets only when they differ from the default, and the prompt may leave them dirty. The kitty part stays out: the pop already restored the app's flags.
6. **An app DSR 5n in flight at T1.** The spec says the app still gets exactly one `CSI 0n`, but `InputMux` consumes the first `CSI 0n`, which is the app's reply. Fix: while the prompt owns stdin, `InputMux` forwards any `CSI 0n` to the container. The prompt's queries are filtered, so a `CSI 0n` then can only answer the app. The app gets one either way, because the bytes are the same.
7. **The barrier reply is matched across reads only while draining.** Elsewhere, including the late-reply strip, matching is per read, so a held `ESC` never delays the Escape key.
8. **Shadow failure while detached:** the Error Handling section (raw replay if allowed, else `ESC c` plus a SIGWINCH nudge) wins over the row in the raw-path table. Raw replay doesn't need the shadow.
9. **Bubble Tea sees no TTY.** Its input is a pipe and its output is the filter, so the program gets `WithWindowSize`, `WithColorProfile` (detected on the real stdout), `WithEnvironment` (for `TERM`) and `WithoutSignalHandler`. A resize while prompting is sent to it as `tea.WindowSizeMsg`. Bubble Tea's input reader for a non-file `io.Reader` can't be cancelled, so `Release` closes the prompt pipe to end it.
10. **`prompt.log`** isn't in the kitty proof of concept. It lives at `$XDG_STATE_HOME/vibepit/prompt-logs/<session>.log`, beside the session directories, because those are removed when the session stops. Capped at 1 MiB; logs untouched for 7 days are removed.
11. **A barrier timeout re-arms the target.** Nothing was shown, so the poller forgets the target and prompts on its next block.
12. **Sessions without `--prompt` skip the emulator** (`overlay.Config.NoShadow`), but still use the same I/O path.
13. **`--prompt` defaults to off** until the manual terminal matrix (step 3) passes.
14. **`CAN` doesn't abort an OSC in libghostty, and so in ghostty:** `ESC ]2;stall CAN` sets the title to `stall`. After a forced cut the real terminal's title and working directory are unknown, so the snapshot writes both unconditionally. Test: `TestForcedCutAfterTheTimeBudget`.
15. **`InputMux.Drain` must run before the barrier query is written.** A local terminal can answer before the next statement; with the query first, the reply went to the container and the prompt timed out. Found as a flake in the plan's own test run. `cutLocked` drains first.
16. **Bubble Tea's renderer writes a bare LF for a new line** when its input isn't a TTY (ultraviolet's `mapNl`), counting on the TTY's ONLCR. `term.MakeRaw` turns ONLCR off, so the prompt would stair-step. The filter puts a CR in front of a bare LF. Tests: `TestFilter/bare_LF_gets_a_CR` (Task 6), `TestApproveScreen_RendersThroughPromptFilter` (Task 12).

The plan's code was compiled and run on a scratch copy of this branch before handing it over: `go vet ./...`, `go test ./...` (overlay 60 times in a row), `make test-integration`'s test, and `-race` on overlay, container, cmd and proxy all pass. Findings 3, 14, 15 and 16 came out of that run.

## Review Focus

1. **Escape key while the prompt owns stdin.** A lone `ESC` must reach the prompt at once, not wait for the next byte. Test: `TestInputMuxPromptGetsEscapeRightAway` (Task 5).
2. **Resize while the prompt shows.** The prompt redraws at the new size, and the leave uses a snapshot at the new size. Test: `TestResizeWhilePrompting` (Task 10).
3. **The agent floods output during a prompt** (`yes`), past the raw log cap. No stall, the log is dropped, the snapshot restores the screen. Test: `TestShowFloodPastLogCap` (Task 10).
4. **A burst of more than 25 blocked targets between two polls.** Each prompts once; none is lost to the tail window of `/logs`. Test: `TestRunBlockPrompter_BurstPastTail` (Task 12).
5. **A prompt requested after the session ended.** `Show` returns `ErrClosed` at once and writes nothing. Test: `TestShowAfterRunReturned` (Task 10).

---

## File Structure

```
proxy/
  target.go, target_test.go        port (new): Target, ParseTarget, LogEntry.Target
  deny.go, deny_test.go            port (new): DenySet
  api.go, api_test.go              port: GET /check, POST /deny, strict /logs?after=N
  log.go, log_test.go              port: Tail, strict EntriesAfter
integration_test.go                modify: /check and /deny over mTLS
tui/
  sanitize.go, sanitize_test.go    port (new): SanitizeText
ward/bar.go                        port: use tui.SanitizeText
cmd/
  allow.go                         port: allowEntry
  control.go, control_test.go      Check, Deny, post helper; testProxy helper (port, no CredDir)
  monitor_ui.go, monitor_ui_test.go  port: allowEntry, SanitizeText, strict polling
  approve_ui.go, approve_ui_test.go  port (new): approveScreen
  prompt.go, prompt_test.go        new: --prompt, blockWatcher, poller, prompter, loggedPrompt
  promptlog.go, promptlog_test.go  new: per-session prompt log
  run.go                           modify: --prompt wiring
overlay/
  doc.go                           package doc, ErrUnavailable, ErrBarrierTimeout, ErrClosed
  pipe.go, pipe_test.go            bufPipe: a pipe whose Write never blocks
  input.go, input_test.go          InputMux: stdin routing, barrier, T2/T3 switches
  filter.go, filter_test.go        NewFilter: the prompt output whitelist
  restore.go, restore_test.go      cut capture, enter, raw/snapshot/reset leave sequences
  terminal.go, terminal_test.go    Config, New, Run, output pump, Resize
  detach.go, detach_test.go        T1 at ground, forced cut, resync, leave path choice
  show.go, show_test.go            Show: T2, the Bubble Tea program, T3
  helpers_test.go                  syncBuffer, chunkReader, vt helpers, state comparison
  harness_test.go                  harness: a Terminal wired to a stand-in vt.Terminal
container/
  terminal.go, terminal_test.go    AttachOption, WithTerminal, WithLogf, I/O via overlay.Terminal
  client.go                        modify: AttachAndStartSession/ExecSession take AttachOption
docs/content/reference/cli.md      --prompt, "Blocked connection prompt"
docs/content/explanations/threat-model.md  "Blocked connection prompts"
AGENTS.md, Makefile                overlay package, --prompt, test-race packages
```

---

### Task 1: Proxy targets, deny set, `/check`, `/deny` and a strict log cursor

Port from `add/kitty-prompt`. `git diff main add/kitty-prompt -- proxy/` shows no drift in these files other than the port, so whole-file checkouts are exact.

**Files:**
- Create: `proxy/target.go`, `proxy/target_test.go`, `proxy/deny.go`, `proxy/deny_test.go`
- Modify: `proxy/api.go`, `proxy/api_test.go`, `proxy/log.go`, `proxy/log_test.go`, `integration_test.go`

**Interfaces:**
- Produces: `type proxy.Target struct{ Source Source; Host, Port string }`, `func (proxy.LogEntry) Target() Target`, `func (Target) String() string` (IPv6 hosts bracketed), `func proxy.ParseTarget(source, s string) (Target, error)` (lowercases hosts), `type proxy.DenySet` (`Add(Target)`, `Denied(Target) bool`, zero value ready), `const proxy.TailSize = 25`, `func (*LogBuffer) Tail(n int) []LogEntry`, `func (*LogBuffer) EntriesAfter(afterID uint64) []LogEntry` (strict: 0 means every buffered entry). Control API: `GET /check?source=proxy|dns&target=…` → `{"allowed":bool,"denied":bool}`; `POST /deny` with `{"source","target"}` → `{"denied":"…"}`; `GET /logs` without `after` → the last 25; `GET /logs?after=N` → every entry with ID > N, including N=0.

- [ ] **Step 1: Bring over the tests**

```bash
git checkout add/kitty-prompt -- proxy/target_test.go proxy/deny_test.go proxy/api_test.go proxy/log_test.go
```

What they pin: `TestTargetString`, `TestParseTarget`, `TestParseTarget_RoundTrip` (IPv6 brackets, lowercase, proxy targets need a port); `TestDenySet` (port and source are part of the target); `TestControlAPI_Check` (live allowlist, bad requests are 400), `TestControlAPI_Deny` (equivalent spelling `A.COM:443` matches), `TestControlAPI_LogsCursor` (`/logs` → 25, `after=0` → 30, `after=27` → 3); `TestTail` and the reworked `TestEntriesAfter` (`EntriesAfter(0)` returns all 30).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./proxy/`
Expected: FAIL to compile: `undefined: Target`, `undefined: DenySet`, `undefined: ParseTarget`, `buf.Tail undefined`.

- [ ] **Step 3: Bring over the implementation**

```bash
git checkout add/kitty-prompt -- proxy/target.go proxy/deny.go proxy/api.go proxy/log.go
```

The changes, for review. `proxy/api.go` gains a `denied DenySet` field, two routes and a strict `/logs`:

```go
	api.mux.HandleFunc("GET /check", api.handleCheck)
	api.mux.HandleFunc("POST /deny", api.handleDeny)
```

```go
// handleLogs returns a recent tail when called without a cursor, and every
// entry with a larger ID for "after=N", including N=0.
func (a *ControlAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	var q url.Values
	if r.URL != nil {
		q = r.URL.Query()
	}
	if !q.Has("after") {
		writeJSON(w, a.log.Tail(TailSize))
		return
	}
	afterID, _ := strconv.ParseUint(q.Get("after"), 10, 64)
	writeJSON(w, a.log.EntriesAfter(afterID))
}
```

`handleCheck` parses `source`/`target` with `ParseTarget` (400 on error), asks `httpAllowlist.Allows(host, port)` or `dnsAllowlist.Allows(host)`, and writes `{"allowed":…,"denied":a.denied.Denied(t)}`. `handleDeny` decodes `{"source","target"}`, parses it (400 on bad JSON or target), calls `a.denied.Add(t)` and writes `{"denied":t.String()}`. `proxy/log.go` replaces the scan in `EntriesAfter` with `lastLocked(n)`, which copies the newest `n` entries out of the ring; IDs are contiguous, so `EntriesAfter(id)` is `lastLocked(min(lastID-id, cap))`.

- [ ] **Step 4: Run the proxy tests**

Run: `go test ./proxy/`
Expected: PASS.

- [ ] **Step 5: Pin `/check` and `/deny` over mTLS in the integration test**

In `integration_test.go`, at the end of `TestProxyServerIntegration`, after the `blockedResp` assertion, add:

```go
	// A blocked target is neither allowed nor denied until a user decides.
	checkURL := fmt.Sprintf("https://127.0.0.1:%d/check?source=proxy&target=evil.com:80", controlPort)
	checkResp, err := tlsClient.Get(checkURL)
	require.NoError(t, err, "control API check")
	defer checkResp.Body.Close()
	var check struct{ Allowed, Denied bool }
	require.NoError(t, json.NewDecoder(checkResp.Body).Decode(&check))
	assert.Equal(t, struct{ Allowed, Denied bool }{}, check)

	denyResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/deny", controlPort), `{"source":"proxy","target":"evil.com:80"}`)
	defer denyResp.Body.Close()
	assert.Equal(t, http.StatusOK, denyResp.StatusCode, "control API deny status")

	deniedResp, err := tlsClient.Get(checkURL)
	require.NoError(t, err, "control API check after deny")
	defer deniedResp.Body.Close()
	require.NoError(t, json.NewDecoder(deniedResp.Body).Decode(&check))
	assert.True(t, check.Denied, "deny is recorded")
	assert.False(t, check.Allowed, "deny doesn't allow")
```

`encoding/json` is already imported there.

- [ ] **Step 6: Run the integration tests**

Run: `make test-integration`
Expected: PASS, including `TestProxyServerIntegration`. The blocked request goes through the local proxy, not the internet.

- [ ] **Step 7: Commit**

```bash
gofmt -l proxy integration_test.go
git add proxy/target.go proxy/target_test.go proxy/deny.go proxy/deny_test.go proxy/api.go proxy/api_test.go proxy/log.go proxy/log_test.go integration_test.go
git commit -m "Add /check and /deny to the control API and a strict log cursor

Ported from the kitty prompt proof of concept (add/kitty-prompt).

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

`gofmt -l` must print nothing.

---

### Task 2: `tui.SanitizeText`

**Files:**
- Create: `tui/sanitize.go`, `tui/sanitize_test.go`
- Modify: `ward/bar.go`

**Interfaces:**
- Produces: `func tui.SanitizeText(s string) string`: drops C0 except tab, DEL, C1 and invalid UTF-8. Used by `approveScreen`, the monitor and `loggedPrompt`.

- [ ] **Step 1: Bring over the test**

```bash
git checkout add/kitty-prompt -- tui/sanitize_test.go
```

It pins: tab kept, `evil\x1b[2J.com` → `evil[2J.com`, CR/LF and other C0 dropped, DEL dropped, `\u009b` dropped, the raw byte `\x9b` (invalid UTF-8) dropped, unicode kept.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./tui/ -run TestSanitizeText`
Expected: FAIL to compile: `undefined: SanitizeText`.

- [ ] **Step 3: Bring over the implementation and the ward change**

```bash
git checkout add/kitty-prompt -- tui/sanitize.go ward/bar.go
gofmt -w ward/bar.go
```

`tui/sanitize.go`:

```go
package tui

import (
	"strings"
	"unicode/utf8"
)

// SanitizeText strips C0 (except tab), C1, DEL, and invalid UTF-8 from text
// that did not originate with the user, e.g. domains and reasons logged from
// sandbox requests, so it cannot inject terminal escape sequences.
func SanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}
```

`ward/bar.go` replaces its private `sanitizeMessage` with `tui.SanitizeText` in `RenderStatusBar` and `RenderCommandBar` and deletes `sanitizeMessage`. The `gofmt -w` adds the trailing newline the proof of concept's file lacks.

- [ ] **Step 4: Run the tests**

Run: `go test ./tui/ ./ward/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tui/sanitize.go tui/sanitize_test.go ward/bar.go
git commit -m "Add tui.SanitizeText for untrusted text

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Control client `Check` and `Deny`, shared `allowEntry`, strict monitor polling

**Files:**
- Modify: `cmd/control.go`, `cmd/control_test.go`, `cmd/allow.go`, `cmd/monitor_ui.go`, `cmd/monitor_ui_test.go`

**Interfaces:**
- Consumes: Task 1's control API, `proxy.LogEntry.Target()`; Task 2's `tui.SanitizeText`.
- Produces: `type CheckResult struct{ Allowed, Denied bool }` with `Decided() bool`; `func (*ControlClient) Check(proxy.LogEntry) (CheckResult, error)`; `func (*ControlClient) Deny(proxy.LogEntry) error`; `func allowEntry(client *ControlClient, session *SessionInfo, entry proxy.LogEntry, save bool) (allowStatus, error)`. Test helpers in `cmd/control_test.go`: `func testControlClient(t *testing.T, h http.Handler) *ControlClient`, `type testProxy struct{ log *proxy.LogBuffer; http *proxy.HTTPAllowlist; dns *proxy.DNSAllowlist; api *proxy.ControlAPI; client *ControlClient }`, `func newTestProxy(t *testing.T) *testProxy`.

- [ ] **Step 1: Bring over the tests**

```bash
git checkout add/kitty-prompt -- cmd/control_test.go cmd/monitor_ui_test.go
```

They pin: `TestControlClient_CheckAndDeny`; `Logs()` returns the last 25 and `LogsAfter(0)` all 30; the monitor's first poll asks for the tail (`/logs` with no query) and later polls are strict (`TestMonitorScreen_PollUsesStrictCursorAfterFirstLoad` expects the queries `""` then `"after=0"`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/ -run 'TestControlClient|TestMonitorScreen'`
Expected: FAIL to compile: `client.Check undefined`, `CheckResult undefined`, `s.loaded undefined`, `too many arguments` / `not enough arguments in call to s.pollLogsCmd`.

- [ ] **Step 3: Add `Check`, `Deny` and the `post` helper to `cmd/control.go`**

Add `"net/url"` to the imports. Keep `NewControlClient` as it is: the proof of concept's `CredDir` exists only for the kitty child process. Add a doc comment on `Logs` and `LogsAfter`, then replace `postAllow` with:

```go
// Logs returns a recent tail of the log, for filling a screen on first load.
```

```go
// LogsAfter returns every entry with an ID greater than afterID; 0 means all.
```

```go
// CheckResult is the proxy's view of a blocked target: allowed by the live
// allowlist, or denied by a user. Both mean nobody needs to be asked.
type CheckResult struct {
	Allowed bool `json:"allowed"`
	Denied  bool `json:"denied"`
}

// Decided reports whether a user decision exists for the target.
func (r CheckResult) Decided() bool { return r.Allowed || r.Denied }

// Check asks the proxy whether the entry's target is allowed or denied.
func (c *ControlClient) Check(entry proxy.LogEntry) (CheckResult, error) {
	q := url.Values{}
	q.Set("source", string(entry.Source))
	q.Set("target", entry.Target().String())
	var res CheckResult
	if err := c.get("/check?"+q.Encode(), &res); err != nil {
		return CheckResult{}, err
	}
	return res, nil
}

// Deny records that the user refused the entry's target, so other clients
// stop prompting for it.
func (c *ControlClient) Deny(entry proxy.LogEntry) error {
	return c.post("/deny", map[string]string{
		"source": string(entry.Source),
		"target": entry.Target().String(),
	}, nil)
}

func (c *ControlClient) postAllow(path string, entries []string) ([]string, error) {
	var result struct {
		Added []string `json:"added"`
	}
	if err := c.post(path, map[string]any{"entries": entries}, &result); err != nil {
		return nil, err
	}
	return result.Added, nil
}

// post sends body as JSON and decodes the response into dest unless it is nil.
func (c *ControlClient) post(path string, body, dest any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", path, err)
	}
	resp, err := c.http.Post(c.baseURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	if dest == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Bring over `allowEntry` and the monitor changes**

```bash
git checkout add/kitty-prompt -- cmd/allow.go cmd/monitor_ui.go
```

For review: `cmd/allow.go` gains

```go
// allowEntry adds the entry's target to the running proxy's allowlist and,
// when save is set, persists it to the project config. Shared by the monitor
// and approve screens.
func allowEntry(client *ControlClient, session *SessionInfo, entry proxy.LogEntry, save bool) (allowStatus, error)
```

(the body is the old `monitorScreen.allowCmd` logic, keyed on `entry.Target().String()`). `cmd/monitor_ui.go` deletes `allowValueForEntry`, makes `allowCmd` call `allowEntry`, adds `loaded bool` (first poll done), makes `pollLogsCmd()` take no argument and call `Logs()` until `loaded` and `LogsAfter(s.pollCursor)` after, sets `s.loaded = true` on a successful poll, and renders host and reason through `tui.SanitizeText`. The `config` import goes away.

- [ ] **Step 5: Run the cmd tests**

Run: `go test ./cmd/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l cmd
git add cmd/control.go cmd/control_test.go cmd/allow.go cmd/monitor_ui.go cmd/monitor_ui_test.go
git commit -m "Add Check and Deny to the control client and share allowEntry

Ported from add/kitty-prompt, without the kitty-only credential directory.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `approveScreen`

**Files:**
- Create: `cmd/approve_ui.go`, `cmd/approve_ui_test.go`

**Interfaces:**
- Consumes: `allowEntry`, `CheckResult`, `ControlClient.Check`/`Deny` (Task 3), `tui.SanitizeText` (Task 2), `pollInterval` (`cmd/monitor_ui.go`), `newTestProxy`, `footerKeyDescs` (tests).
- Produces: `type approveScreen` implementing `tui.Screen`; `func newApproveScreen(session *SessionInfo, client *ControlClient, entry proxy.LogEntry) *approveScreen`; messages `checkResultMsg{res CheckResult; err error}`, `decisionResultMsg{err error}`. Keys: `a` allow for the session, `A` allow and save, `n` deny, `esc`/`q`/`ctrl+c` dismiss without a decision. It quits by itself when a check shows another client decided, unless an error from the user's own decision is showing.

- [ ] **Step 1: Bring over the test**

```bash
git checkout add/kitty-prompt -- cmd/approve_ui_test.go
```

It pins: the view shows target and reason and strips escape sequences from both; footer keys; `a`/`A` allow and `A` also saves to the project config; `n` records a deny in the proxy; `esc`/`q` record nothing; DNS entries go to the DNS allowlist; errors keep the prompt open; a check result closes it only without a pending error; no check while busy.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./cmd/ -run TestApproveScreen`
Expected: FAIL to compile: `undefined: newApproveScreen`, `undefined: approveScreen`.

- [ ] **Step 3: Bring over the screen**

```bash
git checkout add/kitty-prompt -- cmd/approve_ui.go
```

Then change its doc comment, which still names the kitty overlay:

```go
// approveScreen is a single-question prompt shown over the session when the
// proxy blocks a request: allow for the session, allow and save, or deny.
type approveScreen struct {
```

- [ ] **Step 4: Run the tests**

Run: `go test ./cmd/ -run TestApproveScreen -v`
Expected: PASS for every `TestApproveScreen_*`.

- [ ] **Step 5: Commit**

```bash
git add cmd/approve_ui.go cmd/approve_ui_test.go
git commit -m "Add the approve screen for blocked connections

Ported from add/kitty-prompt.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `overlay` package, `bufPipe` and `InputMux`

**Files:**
- Create: `overlay/doc.go`, `overlay/pipe.go`, `overlay/input.go`
- Test: `overlay/pipe_test.go`, `overlay/input_test.go`, `overlay/helpers_test.go`

**Interfaces:**
- Produces: `var overlay.ErrUnavailable, overlay.ErrBarrierTimeout, overlay.ErrClosed`; unexported `type bufPipe` (`newBufPipe(max int) *bufPipe`, `Write` never blocks and drops past `max`, `Read` blocks, `Close` makes `Read` return `io.EOF` once drained); `const barrierQuery = "\x1b[5n"`, `const barrierReply = "\x1b[0n"`; `type InputMux` with `NewInputMux(src io.Reader, container io.Writer) *InputMux`, `Run() error` (nil at EOF), `Done() <-chan struct{}`, `Drain()` (T1), `AwaitBarrier(ctx, timeout) (io.Reader, error)` (T2), `AwaitSilence(ctx, quiet) (io.Reader, error)` (degraded T2), `Release(strip time.Duration, leave func())` (T3). Test helpers (package `overlay`, `helpers_test.go`): `type syncBuffer` (`Write`, `String`), `type chunkReader` (`newChunkReader()`, `Read`, `push(chunk string) bool`, `send(t, chunk)`, `close()`), `readN(t, r, n) string`.

- [ ] **Step 1: Write the test helpers**

`overlay/helpers_test.go`:

```go
package overlay

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer the pump goroutines can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// chunkReader hands its reader one queued chunk per Read, so tests control
// read boundaries. push returns once the reader is back for the next
// chunk, which means it has handled this one.
type chunkReader struct {
	chunks  chan []byte
	next    chan struct{}
	closed  chan struct{}
	once    sync.Once
	pending []byte
	started bool
}

func newChunkReader() *chunkReader {
	return &chunkReader{
		chunks: make(chan []byte),
		next:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.started {
			select {
			case r.next <- struct{}{}:
			case <-r.closed:
				return 0, io.EOF
			}
		}
		select {
		case c := <-r.chunks:
			r.pending, r.started = c, true
		case <-r.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// push hands chunk to the reader and waits until it has handled it. It
// reports false after a timeout or once the reader is closed, and is safe
// from any goroutine.
func (r *chunkReader) push(chunk string) bool {
	select {
	case r.chunks <- []byte(chunk):
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
	select {
	case <-r.next:
		return true
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}

// send is push for the test goroutine.
func (r *chunkReader) send(t *testing.T, chunk string) {
	t.Helper()
	require.True(t, r.push(chunk), "reader didn't handle %q", chunk)
}

// close makes the reader's next Read return io.EOF.
func (r *chunkReader) close() { r.once.Do(func() { close(r.closed) }) }

// readN reads exactly n bytes from r, failing after a timeout.
func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, buf)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("reading %d bytes timed out", n)
	}
	return string(buf)
}
```

- [ ] **Step 2: Write the failing tests for `bufPipe`**

`overlay/pipe_test.go`:

```go
package overlay

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufPipe(t *testing.T) {
	t.Run("read what was written", func(t *testing.T) {
		p := newBufPipe(16)
		_, err := p.Write([]byte("abc"))
		require.NoError(t, err)
		assert.Equal(t, "abc", readN(t, p, 3))
	})

	t.Run("write never blocks and drops past max", func(t *testing.T) {
		p := newBufPipe(4)
		n, err := p.Write([]byte("abcdef"))
		require.NoError(t, err)
		assert.Equal(t, 6, n, "a full pipe still reports the whole write")
		_, _ = p.Write([]byte("g"))
		require.NoError(t, p.Close())
		rest, err := io.ReadAll(p)
		require.NoError(t, err)
		assert.Equal(t, "abcd", string(rest))
	})

	t.Run("close unblocks a reader with EOF", func(t *testing.T) {
		p := newBufPipe(4)
		done := make(chan error, 1)
		go func() {
			_, err := p.Read(make([]byte, 1))
			done <- err
		}()
		require.NoError(t, p.Close())
		assert.ErrorIs(t, <-done, io.EOF)
	})

	t.Run("write after close fails", func(t *testing.T) {
		p := newBufPipe(4)
		require.NoError(t, p.Close())
		_, err := p.Write([]byte("x"))
		assert.ErrorIs(t, err, io.ErrClosedPipe)
	})
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./overlay/`
Expected: FAIL to compile: `undefined: newBufPipe`.

- [ ] **Step 4: Write the package doc and `bufPipe`**

`overlay/doc.go`:

```go
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
```

`overlay/pipe.go`:

```go
package overlay

import (
	"io"
	"sync"
)

// bufPipe is an in-memory pipe whose Write never blocks, so the stdin pump
// can hand bytes to a prompt that isn't reading yet without stalling.
// Writes past max buffered bytes are dropped.
type bufPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	max    int
	closed bool
}

func newBufPipe(max int) *bufPipe {
	p := &bufPipe{max: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Write buffers what fits and reports the whole of b as written. It fails
// only after Close.
func (p *bufPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	if room := p.max - len(p.buf); room > 0 {
		p.buf = append(p.buf, b[:min(len(b), room)]...)
		p.cond.Broadcast()
	}
	return len(b), nil
}

// Read blocks until data is buffered or the pipe is closed. After Close it
// returns what is left, then io.EOF.
func (p *bufPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *bufPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
	return nil
}
```

- [ ] **Step 5: Run the pipe tests**

Run: `go test ./overlay/ -run TestBufPipe -v`
Expected: PASS.

- [ ] **Step 6: Write the failing tests for `InputMux`**

`overlay/input_test.go`:

```go
package overlay

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// muxHarness runs an InputMux over a chunkReader, recording what reaches
// the container.
type muxHarness struct {
	mux  *InputMux
	src  *chunkReader
	cont *syncBuffer
	err  chan error
}

func newMuxHarness(t *testing.T) *muxHarness {
	t.Helper()
	h := &muxHarness{src: newChunkReader(), cont: &syncBuffer{}, err: make(chan error, 1)}
	h.mux = NewInputMux(h.src, h.cont)
	go func() { h.err <- h.mux.Run() }()
	t.Cleanup(func() {
		h.src.close()
		select {
		case <-h.mux.Done():
		case <-time.After(5 * time.Second):
			t.Error("Run didn't return")
		}
	})
	return h
}

// barrier drains and completes T2 with a lone barrier reply.
func (h *muxHarness) barrier(t *testing.T) io.Reader {
	t.Helper()
	h.mux.Drain()
	h.src.send(t, barrierReply)
	in, err := h.mux.AwaitBarrier(context.Background(), time.Second)
	require.NoError(t, err)
	return in
}

func TestInputMuxRoutesToContainer(t *testing.T) {
	h := newMuxHarness(t)
	h.src.send(t, "ls\r")
	h.src.send(t, "\x1b[A")
	h.src.send(t, barrierReply)
	assert.Equal(t, "ls\r\x1b[A"+barrierReply, h.cont.String(), "an app's DSR 5n reply passes while attached")
}

func TestInputMuxBarrier(t *testing.T) {
	tests := []struct {
		name          string
		chunks        []string
		wantContainer string
		wantPrompt    string
	}{
		{name: "reply alone", chunks: []string{"\x1b[0n"}},
		{name: "keys around the reply", chunks: []string{"ab\x1b[0ncd"}, wantContainer: "ab", wantPrompt: "cd"},
		{name: "reply split across reads", chunks: []string{"a\x1b[", "0nb"}, wantContainer: "a", wantPrompt: "b"},
		{name: "reply split at every byte", chunks: []string{"\x1b", "[", "0", "n", "x"}, wantPrompt: "x"},
		{name: "another reply before the barrier", chunks: []string{"\x1b[12;5R\x1b[0n"}, wantContainer: "\x1b[12;5R"},
		{name: "escape key before the reply", chunks: []string{"\x1b", "\x1b[0n"}, wantContainer: "\x1b"},
		{name: "arrow key split before the reply", chunks: []string{"\x1b[", "A\x1b[0nq"}, wantContainer: "\x1b[A", wantPrompt: "q"},
		{name: "app DSR 5n in flight gets one reply", chunks: []string{"\x1b[0n", "\x1b[0n"}, wantContainer: "\x1b[0n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMuxHarness(t)
			h.mux.Drain()
			for _, c := range tt.chunks {
				h.src.send(t, c)
			}
			in, err := h.mux.AwaitBarrier(context.Background(), time.Second)
			require.NoError(t, err)
			assert.Equal(t, tt.wantContainer, h.cont.String())
			if tt.wantPrompt != "" {
				assert.Equal(t, tt.wantPrompt, readN(t, in, len(tt.wantPrompt)))
			}
			h.mux.Release(0, func() {})
			rest, err := io.ReadAll(in)
			require.NoError(t, err)
			assert.Empty(t, string(rest), "nothing else reached the prompt")
		})
	}
}

func TestInputMuxPromptGetsEscapeRightAway(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, "\x1b")
	assert.Equal(t, "\x1b", readN(t, in, 1))
}

func TestInputMuxReleaseHandsInputBack(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, "p")
	assert.Equal(t, "p", readN(t, in, 1))
	h.mux.Release(0, func() {})
	h.src.send(t, "c")
	assert.Equal(t, "c", h.cont.String())
	_, err := in.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "release closes the prompt's input")
}

func TestInputMuxReleaseRunsLeaveUnderLock(t *testing.T) {
	h := newMuxHarness(t)
	h.barrier(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	released := make(chan struct{})
	go func() {
		h.mux.Release(0, func() {
			close(started)
			<-proceed
		})
		close(released)
	}()
	<-started
	routed := make(chan bool, 1)
	go func() { routed <- h.src.push("k") }()
	select {
	case <-routed:
		t.Fatal("input was routed while leave ran")
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	<-released
	require.True(t, <-routed)
	assert.Equal(t, "k", h.cont.String(), "input after T3 goes to the container")
}

func TestInputMuxReleaseFlushesHeldBytes(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	h.src.send(t, "\x1b[")
	assert.Empty(t, h.cont.String(), "a possible reply prefix waits while draining")
	h.mux.Release(0, func() {})
	assert.Equal(t, "\x1b[", h.cont.String())
}

func TestInputMuxStripsLateBarrierReply(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	_, err := h.mux.AwaitBarrier(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, err, ErrBarrierTimeout)
	h.mux.Release(time.Second, func() {})
	h.src.send(t, "a\x1b[0nb")
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "ab\x1b[0n", h.cont.String(), "only one reply is the barrier's")
}

func TestInputMuxStripWindowExpires(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	_, err := h.mux.AwaitBarrier(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, err, ErrBarrierTimeout)
	h.mux.Release(20*time.Millisecond, func() {})
	time.Sleep(50 * time.Millisecond)
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "\x1b[0n", h.cont.String())
}

func TestInputMuxNoStripOnceTheReplyArrived(t *testing.T) {
	h := newMuxHarness(t)
	h.barrier(t)
	h.mux.Release(time.Second, func() {})
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "\x1b[0n", h.cont.String(), "a later reply belongs to the app")
}

func TestInputMuxAwaitBarrierCancelled(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.mux.AwaitBarrier(ctx, time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestInputMuxAwaitSilence(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	h.src.send(t, "k1")
	start := time.Now()
	in, err := h.mux.AwaitSilence(context.Background(), 30*time.Millisecond)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond, "waits for quiet")
	assert.Equal(t, "k1", h.cont.String())
	h.src.send(t, "p")
	assert.Equal(t, "p", readN(t, in, 1))
}

func TestInputMuxEOF(t *testing.T) {
	t.Run("while attached", func(t *testing.T) {
		h := newMuxHarness(t)
		h.src.close()
		<-h.mux.Done()
		assert.NoError(t, <-h.err)
		h.mux.Drain()
		_, err := h.mux.AwaitBarrier(context.Background(), time.Second)
		assert.ErrorIs(t, err, ErrClosed)
	})
	t.Run("while prompting", func(t *testing.T) {
		h := newMuxHarness(t)
		in := h.barrier(t)
		h.src.close()
		_, err := in.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.EOF, "the prompt sees EOF")
	})
}

func TestInputMuxPromptInputNeverBlocks(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, strings.Repeat("x", promptInputMax+100))
	assert.Len(t, readN(t, in, promptInputMax), promptInputMax)
	h.mux.Release(0, func() {})
	rest, err := io.ReadAll(in)
	require.NoError(t, err)
	assert.Empty(t, rest, "the excess was dropped")
}
```

- [ ] **Step 7: Run them to see them fail**

Run: `go test ./overlay/ -run TestInputMux`
Expected: FAIL to compile: `undefined: NewInputMux`, `undefined: barrierReply`, `undefined: promptInputMax`.

- [ ] **Step 8: Write `InputMux`**

`overlay/input.go`:

```go
package overlay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	// barrierQuery is DSR 5n. Terminals answer it with barrierReply, in
	// order with their other replies, so the reply marks the byte where
	// every reply owed to the app has arrived.
	barrierQuery = "\x1b[5n"
	barrierReply = "\x1b[0n"
	// promptInputMax bounds what the prompt's input buffers.
	promptInputMax = 64 << 10
)

type inputMode uint8

const (
	toContainer inputMode = iota
	draining              // T1 to T2: still the container's, watching for the barrier reply
	toPrompt
)

// InputMux is the only reader of stdin. Input goes to the container except
// while a prompt owns it, and it changes hands at exact bytes: at the
// barrier reply (T2) and under the caller's lock (T3).
type InputMux struct {
	src       io.Reader
	container io.Writer
	done      chan struct{}

	mu         sync.Mutex
	mode       inputMode
	held       int // leading bytes of barrierReply seen and not routed yet
	prompt     *bufPipe
	barrier    chan struct{} // closed at the barrier reply
	stripUntil time.Time     // until then, drop one barrier reply
	lastInput  time.Time
}

// NewInputMux routes src to container until a prompt takes the input.
func NewInputMux(src io.Reader, container io.Writer) *InputMux {
	return &InputMux{src: src, container: container, done: make(chan struct{})}
}

// Done is closed when stdin has ended.
func (m *InputMux) Done() <-chan struct{} { return m.done }

// Run reads stdin until it ends. It returns nil at EOF.
func (m *InputMux) Run() error {
	buf := make([]byte, 4096)
	for {
		n, err := m.src.Read(buf)
		if n > 0 {
			m.route(buf[:n])
		}
		if err != nil {
			m.mu.Lock()
			m.flushHeldLocked()
			if m.prompt != nil {
				_ = m.prompt.Close()
			}
			close(m.done)
			m.mu.Unlock()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (m *InputMux) route(p []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.lastInput = now
	if m.mode == toContainer && !now.Before(m.stripUntil) {
		m.write(p)
		return
	}
	var plain []byte
	for _, b := range p {
		if b == barrierReply[m.held] {
			m.held++
			if m.held == len(barrierReply) {
				m.held = 0
				m.write(plain)
				plain = plain[:0]
				m.reply()
			}
			continue
		}
		if m.held > 0 {
			// Not the reply after all: the held prefix is ordinary input.
			plain = append(plain, barrierReply[:m.held]...)
			m.held = 0
		}
		if b == barrierReply[0] {
			m.held = 1
			continue
		}
		plain = append(plain, b)
	}
	// Only the barrier reply is worth waiting for across reads. Anywhere
	// else a held ESC would delay the Escape key.
	if m.mode != draining && m.held > 0 {
		plain = append(plain, barrierReply[:m.held]...)
		m.held = 0
	}
	m.write(plain)
}

// reply handles a complete barrier reply.
func (m *InputMux) reply() {
	switch m.mode {
	case draining:
		// T2: the terminal answers in order, so every reply it owed the
		// app has already gone to the container.
		m.mode = toPrompt
		close(m.barrier)
	case toPrompt:
		// The prompt's queries are filtered out, so this answers a DSR 5n
		// the app sent before the cut. The first CSI 0n went to the
		// barrier; the bytes are the same, so the app still gets one.
		_, _ = m.container.Write([]byte(barrierReply))
	default:
		// The late reply to a barrier query that timed out.
		m.stripUntil = time.Time{}
	}
}

// write sends p to the current owner of the input.
func (m *InputMux) write(p []byte) {
	if len(p) == 0 {
		return
	}
	if m.mode == toPrompt {
		_, _ = m.prompt.Write(p)
		return
	}
	_, _ = m.container.Write(p)
}

func (m *InputMux) flushHeldLocked() {
	if m.held > 0 {
		m.write([]byte(barrierReply[:m.held]))
		m.held = 0
	}
}

// Drain starts T1. Input stays with the container until the barrier
// reply, which AwaitBarrier waits for. Call it before the barrier query
// goes out.
func (m *InputMux) Drain() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = draining
	m.held = 0
	m.stripUntil = time.Time{}
	m.prompt = newBufPipe(promptInputMax)
	m.barrier = make(chan struct{})
}

// AwaitBarrier is T2: it waits for the barrier reply and returns the
// prompt's input. It fails with ErrBarrierTimeout after timeout.
func (m *InputMux) AwaitBarrier(ctx context.Context, timeout time.Duration) (io.Reader, error) {
	m.mu.Lock()
	barrier, prompt := m.barrier, m.prompt
	m.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case <-barrier:
		return prompt, nil
	case <-timer.C:
		err = ErrBarrierTimeout
	case <-ctx.Done():
		err = ctx.Err()
	case <-m.done:
		err = ErrClosed
	}
	select {
	case <-barrier:
		// The reply won the race.
		return prompt, nil
	default:
		return nil, err
	}
}

// AwaitSilence is T2 for a terminal that doesn't answer the barrier
// query: input moves to the prompt after quiet without input. A reply or
// key in flight may land on the wrong side.
func (m *InputMux) AwaitSilence(ctx context.Context, quiet time.Duration) (io.Reader, error) {
	for {
		m.mu.Lock()
		wait := quiet - time.Since(m.lastInput)
		if wait <= 0 {
			m.flushHeldLocked()
			m.mode = toPrompt
			p := m.prompt
			m.mu.Unlock()
			return p, nil
		}
		m.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-m.done:
			timer.Stop()
			return nil, ErrClosed
		}
	}
}

// Release is T3: it runs leave while no input can be routed, then hands
// input back to the container. When the barrier reply is still pending,
// strip drops one that arrives within that time, so it can't reach the
// app.
func (m *InputMux) Release(strip time.Duration, leave func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	leave()
	pending := m.mode == draining
	m.flushHeldLocked()
	m.mode = toContainer
	if m.prompt != nil {
		_ = m.prompt.Close()
		m.prompt = nil
	}
	if strip > 0 && pending {
		m.stripUntil = time.Now().Add(strip)
	}
}
```

- [ ] **Step 9: Run the tests, also under the race detector**

Run: `go test ./overlay/ -v && CGO_ENABLED=1 go test -race ./overlay/`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -l overlay
git add overlay/doc.go overlay/pipe.go overlay/pipe_test.go overlay/input.go overlay/input_test.go overlay/helpers_test.go
git commit -m "Add overlay.InputMux: stdin routing with a barrier handoff

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Prompt output filter

**Files:**
- Create: `overlay/filter.go`
- Test: `overlay/filter_test.go`

**Interfaces:**
- Produces: `func overlay.NewFilter(w io.Writer) io.Writer`. Passes text, BS, HT, LF, CR; CSI without prefix or intermediate with the finals in `csiAllowed` (`HABCDGdEF`, `JKX@PLM`, `mr`, and REP `b`, HPA (a backtick), HPR `a`, VPR `e`, CHT `I`, CBT `Z`, SU `S`, SD `T`); `CSI ?25h/l` and `CSI ?2026h/l` with exactly one parameter; `ESC M`, `ESC D`, `ESC E`. Drops everything else. Holds back a sequence or UTF-8 character split across writes (up to 64 KiB). Puts a CR in front of a bare LF. `Write` reports `len(b)`.

- [ ] **Step 1: Write the failing tests**

`overlay/filter_test.go`:

```go
package overlay

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // empty with keep: the input passes unchanged
		keep bool
	}{
		{name: "text", in: "hello wörld 🙂", keep: true},
		{name: "BS HT CR LF", in: "a\bb\tc\r\nd", keep: true},
		{name: "bare LF gets a CR", in: "a\nb\r\nc\n\nd", want: "a\r\nb\r\nc\r\n\r\nd"},
		{name: "other C0 and DEL dropped", in: "a\x07b\x0ec\x0fd\x00e\x7ff", want: "abcdef"},
		{name: "cursor movement", in: "\x1b[3;4H\x1b[2A\x1b[B\x1b[5C\x1b[D\x1b[7G\x1b[2d\x1b[E\x1b[F", keep: true},
		{name: "erasing and editing", in: "\x1b[2J\x1b[K\x1b[3X\x1b[2@\x1b[P\x1b[L\x1b[2M", keep: true},
		{name: "SGR", in: "\x1b[1;38;2;1;2;3m\x1b[4:3m\x1b[0m", keep: true},
		{name: "scroll region", in: "\x1b[2;10r\x1b[r", keep: true},
		{name: "renderer sequences", in: "x\x1b[5b\x1b[10`\x1b[2a\x1b[3e\x1b[I\x1b[Z\x1b[2S\x1b[T\x1bM\x1bD\x1bE", keep: true},
		{name: "cursor visibility and synchronized output", in: "\x1b[?25l\x1b[?2026h\x1b[?2026l\x1b[?25h", keep: true},
		{name: "alternate screen dropped", in: "\x1b[?1049hx\x1b[?1049l\x1b[?1047h\x1b[?47h", want: "x"},
		{name: "other modes dropped", in: "\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[?1004h\x1b[4h\x1b[20h\x1b[?7l\x1b[?6h\x1b[?25;1049h"},
		{name: "queries dropped", in: "\x1b[c\x1b[>c\x1b[5n\x1b[6n\x1b[?2026$p\x1b[>q\x1b]11;?\x07\x1b[?u\x1b[14t\x1bP+q544e\x1b\\"},
		{name: "keyboard protocols dropped", in: "\x1b[>1u\x1b[<u\x1b[=0;1u\x1b[>4;1m"},
		{name: "OSC dropped", in: "\x1b]0;title\x07\x1b]2;t\x1b\\\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\x1b]52;c;aGk=\x07", want: "link"},
		{name: "saved cursor dropped", in: "\x1b7\x1b8\x1b[s\x1b[u"},
		{name: "charsets dropped", in: "\x1b(0q\x1b)0\x1b(B", want: "q"},
		{name: "cursor style dropped", in: "\x1b[5 q"},
		{name: "APC dropped", in: "\x1b_Gf=24;AAAA\x1b\\"},
		{name: "resets dropped", in: "\x1bc\x1b[!p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			n, err := NewFilter(&out).Write([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, len(tt.in), n)
			want := tt.want
			if tt.keep {
				want = tt.in
			}
			assert.Equal(t, want, out.String())
		})
	}
}

func TestFilterJoinsSplitWrites(t *testing.T) {
	in := "a\x1b[?1049hb\x1b[31mc\x1b]2;x\x07d€e"
	var out bytes.Buffer
	f := NewFilter(&out)
	for i := range len(in) {
		_, err := f.Write([]byte{in[i]})
		require.NoError(t, err)
	}
	assert.Equal(t, "ab\x1b[31mcd€e", out.String())
}

func TestFilterDropsAnOversizedUnfinishedSequence(t *testing.T) {
	var out bytes.Buffer
	f := NewFilter(&out)
	_, err := f.Write([]byte("\x1b]2;" + strings.Repeat("y", filterPendingMax)))
	require.NoError(t, err)
	_, err = f.Write([]byte("\x1b[31mok"))
	require.NoError(t, err)
	assert.Equal(t, "\x1b[31mok", out.String())
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./overlay/ -run TestFilter`
Expected: FAIL to compile: `undefined: NewFilter`, `undefined: filterPendingMax`.

- [ ] **Step 3: Write the filter**

`overlay/filter.go`:

```go
package overlay

import (
	"io"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// filterPendingMax bounds an unfinished sequence the filter holds back.
const filterPendingMax = 64 << 10

// csiAllowed are the finals of CSI sequences without prefix or
// intermediate that pass: cursor movement (CUP CUU CUD CUF CUB CHA VPA CNL
// CPL), erasing and editing (ED EL ECH ICH DCH IL DL), SGR, DECSTBM, and
// the sequences Bubble Tea's renderer picks by TERM, which also only move
// the cursor or edit content: REP HPA HPR VPR CHT CBT SU SD.
const csiAllowed = "HABCDGdEF" + "JKX@PLM" + "mr" + "b`aeIZST"

type filter struct {
	w       io.Writer
	p       *ansi.Parser
	pending []byte
	cr      bool // the last byte passed was CR
}

// NewFilter returns a writer that passes only the output a prompt may send
// to the real terminal: text, BS, HT, LF, CR, cursor movement, erasing and
// editing, SGR, DECSTBM, and modes 25 and 2026. Everything else is
// dropped, every query included, so the prompt changes only state the
// leave restores. A bare LF gets a CR in front, as a TTY with ONLCR would
// add.
func NewFilter(w io.Writer) io.Writer {
	return &filter{w: w, p: ansi.NewParser()}
}

func (f *filter) Write(b []byte) (int, error) {
	data := append(f.pending, b...)
	f.pending = nil
	var out []byte
	for len(data) > 0 {
		if data[0] >= utf8.RuneSelf && !utf8.FullRune(data) {
			// A character split across writes: wait for the rest.
			f.pending = append([]byte(nil), data...)
			break
		}
		seq, _, n, state := ansi.DecodeSequence(data, ansi.NormalState, f.p)
		if n <= 0 {
			break
		}
		if state != ansi.NormalState && n == len(data) {
			// A sequence split across writes: wait for the rest, within
			// reason.
			if len(data) <= filterPendingMax {
				f.pending = append([]byte(nil), data...)
			}
			break
		}
		if allowed(seq, f.p) {
			if len(seq) == 1 && seq[0] == ansi.LF && !f.cr {
				// Bubble Tea's renderer writes a bare LF for a new line
				// when its input isn't a TTY, counting on the TTY to add
				// the CR (ONLCR). The real terminal is in raw mode.
				out = append(out, ansi.CR)
			}
			out = append(out, seq...)
			f.cr = len(seq) == 1 && seq[0] == ansi.CR
		}
		data = data[n:]
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// allowed reports whether seq, just decoded by p, may reach the terminal.
func allowed(seq []byte, p *ansi.Parser) bool {
	switch c := seq[0]; {
	case c == ansi.ESC && len(seq) > 1 && seq[1] == '[':
		return allowedCSI(ansi.Cmd(p.Command()), p.Params())
	case c == ansi.ESC:
		// RI, IND and NEL move the cursor, scrolling at the margins.
		return len(seq) == 2 && strings.IndexByte("MDE", seq[1]) >= 0
	case c < 0x20 || c == ansi.DEL:
		return c == ansi.BS || c == ansi.HT || c == ansi.LF || c == ansi.CR
	default:
		r, _ := utf8.DecodeRune(seq)
		return r >= 0x20 && (r < 0x80 || r >= 0xa0) // no C1 controls
	}
}

func allowedCSI(cmd ansi.Cmd, params ansi.Params) bool {
	if cmd.Intermediate() != 0 {
		return false
	}
	switch cmd.Prefix() {
	case 0:
		return cmd.Final() != 0 && strings.IndexByte(csiAllowed, cmd.Final()) >= 0
	case '?':
		if (cmd.Final() != 'h' && cmd.Final() != 'l') || len(params) != 1 {
			return false
		}
		mode := params[0].Param(0)
		return mode == 25 || mode == 2026
	default:
		return false
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./overlay/ -run TestFilter -v`
Expected: PASS. If a case in `TestFilter` fails because `ansi.DecodeSequence` splits a sequence differently than expected, print `seq` and `ansi.Cmd(p.Command())` for that input before changing the allowlist; the allowlist itself is fixed by the spec and Finding 1.

- [ ] **Step 5: Commit**

```bash
gofmt -l overlay
git add overlay/filter.go overlay/filter_test.go
git commit -m "Add overlay.NewFilter: the prompt output whitelist

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Cut state, enter and leave sequences

Pure functions over `vt.Terminal`: no goroutines, no I/O. The stand-in for the real terminal is a second `vt.Terminal`.

**Files:**
- Create: `overlay/restore.go`
- Modify: `overlay/helpers_test.go`
- Test: `overlay/restore_test.go`

**Interfaces:**
- Consumes: `vt.NewTerminal`, `vt.Terminal` getters, `Format`, `SetMode`, `Continuation`, `vt.ErrContinuationUnavailable`, `vt.AllExtras`.
- Produces: `type modeKey struct{ mode uint16; ansi bool }`; `var modeIRM, modeSync modeKey`; `type cutState struct{ forced, barrierSent, alt bool; modes map[modeKey]bool; extras []byte; title, pwd string; cursor vt.CursorStyle }`; `func captureCut(sh *vt.Terminal) (*cutState, error)`; `type entered struct{ kitty, promptScreen bool }`; `func enterFor(c *cutState) entered`; `func enterSeq(e entered) string`; `func rawLeave(c *cutState, e entered, log []byte) []byte`; `func snapshotLeave(sh *vt.Terminal, c *cutState, e entered) (out []byte, resync bool, err error)`; `func resetLeave(e entered) []byte`; `func modeSeq(k modeKey, on bool) string`; constants `penReset`, `keyboardReset`, `clearScreen`. Test helpers: `newVT`, `feed`, `screenText`, `termState`, `stateOf`, `probeSuffix`, `assertSameTerminal`.

- [ ] **Step 1: Add the terminal test helpers**

Append to `overlay/helpers_test.go`, and add `"github.com/bernd/vibepit/vt"` and `"github.com/stretchr/testify/assert"` to its imports:

```go
func newVT(t *testing.T, cols, rows int, opts ...vt.TerminalOption) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(uint16(cols), uint16(rows), opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func feed(t *testing.T, term *vt.Terminal, s string) {
	t.Helper()
	_, err := term.Write([]byte(s))
	require.NoError(t, err)
}

// screenText is the visible screen as plain text, or "" on error, so it is
// safe inside require.Eventually.
func screenText(term *vt.Terminal) string {
	b, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true, Region: vt.RegionScreen})
	if err != nil {
		return ""
	}
	return string(b)
}

// termState is what a restore must reproduce on the real terminal.
type termState struct {
	Screen string // content, modes, cursor, pen, keyboard state
	Alt    bool
	Kitty  uint8
	Modes  []vt.ModeState
	Title  string
	Pwd    string
	Cursor vt.CursorStyle
}

// stateExtras leave out charsets: libghostty's default designation is
// UTF-8, which ESC ( B doesn't restore, while real terminals treat ESC ( B
// as the default. probeSuffix checks charsets by printing through them.
var stateExtras = func() vt.Extras {
	x := vt.AllExtras
	x.Charsets = false
	return x
}()

func stateOf(t *testing.T, term *vt.Terminal) termState {
	t.Helper()
	var s termState
	screen, err := term.Format(vt.FormatOptions{Unwrap: true, Extras: stateExtras, Region: vt.RegionScreen})
	require.NoError(t, err)
	s.Screen = string(screen)
	s.Alt, err = term.AltScreen()
	require.NoError(t, err)
	s.Kitty, err = term.KittyKeyboardFlags()
	require.NoError(t, err)
	s.Modes, err = term.Modes()
	require.NoError(t, err)
	s.Title, err = term.Title()
	require.NoError(t, err)
	s.Pwd, err = term.Pwd()
	require.NoError(t, err)
	s.Cursor, err = term.CursorStyle()
	require.NoError(t, err)
	return s
}

// probeSuffix shows state the capture can't: the active charset (q draws a
// line in DEC graphics), LNM, and the scroll region (DL).
const probeSuffix = "q\r\nw\x1b[Mz"

// assertSameTerminal compares the state of want and got, then again after
// the same probe output.
func assertSameTerminal(t *testing.T, want, got *vt.Terminal) {
	t.Helper()
	assert.Equal(t, stateOf(t, want), stateOf(t, got))
	feed(t, want, probeSuffix)
	feed(t, got, probeSuffix)
	assert.Equal(t, stateOf(t, want), stateOf(t, got), "after the probe suffix")
}
```

- [ ] **Step 2: Write the failing tests**

`overlay/restore_test.go`:

```go
package overlay

import (
	"strings"
	"testing"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promptDraw is output the filter lets through. It leaves the pen, the
// scroll region, the cursor and its visibility dirty, as a prompt might.
const promptDraw = "\x1b[1;1H\x1b[1;35mPROMPT\x1b[3;4r\x1b[?25l\x1b[2;2Hx"

// throughPrompt feeds real what the real terminal gets from T1 until the
// program exits: the T1 writes, the enter sequence and prompt output.
func throughPrompt(t *testing.T, real *vt.Terminal, c *cutState) entered {
	t.Helper()
	if c.modes[modeSync] {
		feed(t, real, "\x1b[?2026l")
	}
	e := enterFor(c)
	feed(t, real, enterSeq(e))
	feed(t, real, promptDraw)
	return e
}

func mustCut(t *testing.T, sh *vt.Terminal) *cutState {
	t.Helper()
	c, err := captureCut(sh)
	require.NoError(t, err)
	return c
}

func TestCaptureCut(t *testing.T) {
	sh := newVT(t, 20, 5)
	feed(t, sh, "\x1b]2;title\x07\x1b]7;file://h/p\x07\x1b[5 q\x1b[?1049h\x1b[4h\x1b[2;4r\x1b[31mab")
	c := mustCut(t, sh)
	assert.True(t, c.alt)
	assert.True(t, c.modes[modeIRM])
	assert.True(t, c.modes[modeKey{1049, false}])
	assert.False(t, c.modes[modeSync])
	assert.Equal(t, "title", c.title)
	assert.Equal(t, "file://h/p", c.pwd)
	assert.Equal(t, vt.CursorStyle{Shape: vt.CursorBar, Blinking: true}, c.cursor)
	assert.Contains(t, string(c.extras), "\x1b[2;4r", "the scroll region")
	assert.NotContains(t, string(c.extras), "ab", "no content")
}

func TestEnterSeq(t *testing.T) {
	const setD = "\x1b[4l\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7h\x1b[?25h\x1b[?2026l"
	assert.Equal(t, "\x1b[?1047h\x1b[>0u"+setD+penReset+clearScreen, enterSeq(entered{kitty: true, promptScreen: true}))
	assert.Equal(t, "\x1b[>0u"+setD+penReset+clearScreen, enterSeq(entered{kitty: true}), "on the app's alternate screen")
	assert.Equal(t, entered{kitty: true, promptScreen: true}, enterFor(&cutState{}))
	assert.Equal(t, entered{kitty: true}, enterFor(&cutState{alt: true}))
}

func TestRawLeaveOrder(t *testing.T) {
	c := &cutState{
		modes:  map[modeKey]bool{modeIRM: true, {7, false}: false, {25, false}: true, modeSync: true},
		extras: []byte("<extras>"),
	}
	want := "\x1b[<u\x1b[?1047l" +
		"\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7l\x1b[?25h\x1b[?2026h" +
		penReset + "<extras>" +
		"\x1b[4h" +
		"<log>"
	assert.Equal(t, want, string(rawLeave(c, entered{kitty: true, promptScreen: true}, []byte("<log>"))))
}

func TestRawLeaveRestoresTheCut(t *testing.T) {
	tests := []struct{ name, before, detached string }{
		{"scroll region, insert, DEC graphics, red, pending wrap", "\x1b[2;4r\x1b[4h\x1b(0\x1b[31m\x1b[3;1H" + strings.Repeat("x", 20), "q\x1b[0mtail"},
		{"origin mode", "\x1b[2;5r\x1b[?6h\x1b[2;3Hab", "cd"},
		{"line feed mode, reverse video, no wrap", "\x1b[20h\x1b[?5h\x1b[?7lline", "\nnext"},
		{"hidden cursor, bracketed paste, kitty flags", "\x1b[?25l\x1b[?2004h\x1b[>5uab", "\x1b[<ucd"},
		{"synchronized output open", "\x1b[?2026hframe", "\x1b[?2026l"},
		{"hyperlink and protection", "\x1b]8;;http://x\x1b\\link\x1b[1\"q", "\x1b]8;;\x1b\\"},
		{"saved cursor", "ab\x1b7cd", "\x1b8Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, tt.before)
			feed(t, real, tt.before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			feed(t, shadow, tt.detached)
			feed(t, real, string(rawLeave(c, e, []byte(tt.detached))))
			assertSameTerminal(t, shadow, real)
		})
	}
}

func TestSnapshotLeaveRestoresChangesWhileDetached(t *testing.T) {
	tests := []struct {
		name, before, detached string
		later                  string // app output after the leave
	}{
		{name: "app leaves the alternate screen, resets insert and wrap", before: "shell$ \x1b[?1049h\x1b[4h\x1b[?7lvim", detached: "\x1b[4l\x1b[?7h\x1b[?1049lback"},
		{name: "app enters the alternate screen", before: "shell$ ls", detached: "\x1b[?1049hvim", later: "\x1b[?1049l"},
		{name: "title, working directory, cursor style", before: "a", detached: "\x1b]2;new\x07\x1b]7;file://h/tmp\x07\x1b[5 q"},
		{name: "scroll region, origin mode, pen, charset", before: "a", detached: "\x1b[3;6r\x1b[?6h\x1b[1;32mgreen\x1b(0"},
		{name: "kitty flags set", before: "a", detached: "\x1b[=5;1u"},
		{name: "modifyOtherKeys reset", before: "\x1b[>4;2ma", detached: "\x1b[>4;0m"},
		{name: "synchronized output open", before: "a", detached: "\x1b[?2026hframe"},
		{name: "mouse modes on the alternate screen", before: "\x1b[?1049h\x1b[?1002h\x1b[?1006hmenu", detached: "\x1b[?1002l\x1b[?1003h"},
		{name: "lines scrolled off", before: "a", detached: strings.Repeat("line\r\n", 12) + "end"},
		{name: "pending wrap", before: "a", detached: "\x1b[6;1H" + strings.Repeat("w", 20), later: "Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, tt.before)
			feed(t, real, tt.before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			feed(t, shadow, tt.detached)
			out, resync, err := snapshotLeave(shadow, c, e)
			require.NoError(t, err)
			assert.False(t, resync)
			feed(t, real, string(out))
			feed(t, shadow, tt.later)
			feed(t, real, tt.later)
			assertSameTerminal(t, shadow, real)
		})
	}
}

func TestSnapshotLeaveOrder(t *testing.T) {
	shadow := newVT(t, 20, 6)
	feed(t, shadow, "\x1b[?1049hvim")
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?1049l\x1b]2;t\x07shell\x1b[3")
	out, resync, err := snapshotLeave(shadow, c, entered{kitty: true})
	require.NoError(t, err)
	assert.False(t, resync)
	s := string(out)
	last := 0
	for _, part := range []string{"\x1b[<u", "\x1b[?1049l", "\x1b[4l", penReset + keyboardReset, clearScreen, "shell", "\x1b]2;t\x1b\\"} {
		i := strings.Index(s[last:], part)
		require.GreaterOrEqual(t, i, 0, "%q missing after byte %d of %q", part, last, s)
		last += i + len(part)
	}
	assert.True(t, strings.HasSuffix(s, "\x1b[3"), "the continuation comes last")
}

func TestSnapshotLeaveWritesReportModesOnlyWhenChanged(t *testing.T) {
	shadow := newVT(t, 20, 6)
	feed(t, shadow, "\x1b[?1004h\x1b[?2048h")
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?2048l\x1b[?2031h")
	out, _, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	s := string(out)
	assert.NotContains(t, s, "\x1b[?1004", "unchanged: enabling it again would send a focus report")
	assert.NotContains(t, s, "\x1b[?2033", "unchanged")
	assert.Contains(t, s, "\x1b[?2048l")
	assert.Contains(t, s, "\x1b[?2031h")
	assert.Contains(t, s, "\x1b[?2004l", "other modes are written whatever their value")
}

func TestSnapshotLeaveTurnsSynchronizedOutputOff(t *testing.T) {
	shadow := newVT(t, 20, 6)
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?2026h")
	out, _, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	assert.Contains(t, string(out), "\x1b[?2026l")
	on, err := shadow.Mode(2026, false)
	require.NoError(t, err)
	assert.False(t, on, "the shadow matches what the real terminal gets")
}

func TestSnapshotLeaveResyncsPastTheContinuationLimit(t *testing.T) {
	shadow := newVT(t, 20, 6, vt.WithContinuationMaxBytes(16))
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b]2;"+strings.Repeat("y", 100))
	out, resync, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	assert.True(t, resync)
	assert.NotContains(t, string(out), "yyyy")
}

func TestLeaveKeepsTheKittyStack(t *testing.T) {
	for _, alt := range []bool{false, true} {
		name := "primary screen, raw replay"
		if alt {
			name = "alternate screen, snapshot"
		}
		t.Run(name, func(t *testing.T) {
			before := "\x1b[>1u\x1b[>3u"
			if alt {
				before = "\x1b[?1049h" + before
			}
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, before)
			feed(t, real, before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			out := rawLeave(c, e, nil)
			if alt {
				var err error
				out, _, err = snapshotLeave(shadow, c, e)
				require.NoError(t, err)
			}
			feed(t, real, string(out))
			for range 2 {
				feed(t, shadow, "\x1b[<u")
				feed(t, real, "\x1b[<u")
				want, err := shadow.KittyKeyboardFlags()
				require.NoError(t, err)
				got, err := real.KittyKeyboardFlags()
				require.NoError(t, err)
				assert.Equal(t, want, got)
			}
		})
	}
}

func TestResetLeave(t *testing.T) {
	assert.Equal(t, "\x1b[<u\x1b[?1047l\x1bc", string(resetLeave(entered{kitty: true, promptScreen: true})))
	assert.Equal(t, "\x1bc", string(resetLeave(entered{})))
}

func TestOSCText(t *testing.T) {
	assert.Equal(t, "evil[2Jtitle", oscText("evil\x1b[2J\x07title\u009c"))
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./overlay/ -run 'TestCaptureCut|TestEnterSeq|TestRawLeave|TestSnapshotLeave|TestLeaveKeeps|TestResetLeave|TestOSCText'`
Expected: FAIL to compile: `undefined: captureCut`, `undefined: enterSeq`, `undefined: rawLeave`, …

- [ ] **Step 4: Write the sequences**

`overlay/restore.go`:

```go
package overlay

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/bernd/vibepit/vt"
)

// modeKey names a mode: a DEC mode (CSI ? n h) unless ansi.
type modeKey struct {
	mode uint16
	ansi bool
}

var (
	modeIRM  = modeKey{4, true}
	modeSync = modeKey{2026, false}
	// drawModes are the set-D modes except IRM, which the raw leave writes
	// after the cursor, because the pending-wrap reprint must not insert.
	drawModes = []modeKey{{20, true}, {5, false}, {6, false}, {7, false}, {25, false}, modeSync}
)

// skipReconcile are DEC modes a restore must not write: DECCOLM clears the
// screen, 1048 saves the cursor, and the screen modes belong to the screen
// switch.
var skipReconcile = map[uint16]bool{3: true, 47: true, 1047: true, 1048: true, 1049: true}

// reportModes make some terminals send a report as soon as they are
// enabled: focus (1004), colour scheme (2031), visibility (2033) and
// in-band size (2048). A snapshot writes them only when they changed, so
// the app gets each report once, from the real terminal.
var reportModes = map[uint16]bool{1004: true, 2031: true, 2033: true, 2048: true}

const (
	// penReset clears what the formatter emits only when it differs from
	// the default: the scroll region, SGR, an open hyperlink, the charsets
	// and GL.
	penReset = "\x1b[r\x1b[0m\x1b]8;;\x1b\\\x1b(B\x1b)B\x1b*B\x1b+B\x0f"
	// keyboardReset is the snapshot's addition: the kitty keyboard flags
	// and modifyOtherKeys, which the formatter also emits only when set.
	// The raw leave must not write it, because its pop already restored
	// the app's flags.
	keyboardReset = "\x1b[=0;1u\x1b[>4;0m"
	clearScreen   = "\x1b[2J\x1b[H"
	ris           = "\x1bc"
)

// cutState is the real terminal's state at the cut (T1), read from the
// shadow. The prompt changes only set D, so everything else stays the
// real terminal's known state until the leave.
type cutState struct {
	forced      bool // no ground within the budget; CAN was written
	barrierSent bool
	alt         bool
	modes       map[modeKey]bool
	extras      []byte // scroll region, cursor, pen, hyperlink, protection, charsets
	title, pwd  string
	cursor      vt.CursorStyle
}

// cutExtras restore the set-D state the modes don't cover. The Modes
// extra is left out: the leave writes set-D modes itself.
var cutExtras = vt.Extras{ScrollRegion: true, Cursor: true, Style: true, Hyperlink: true, Protection: true, Charsets: true}

func captureCut(sh *vt.Terminal) (*cutState, error) {
	c := &cutState{modes: make(map[modeKey]bool)}
	var err error
	if c.alt, err = sh.AltScreen(); err != nil {
		return nil, err
	}
	modes, err := sh.Modes()
	if err != nil {
		return nil, err
	}
	for _, m := range modes {
		c.modes[modeKey{m.Mode, m.ANSI}] = m.Value
	}
	if c.extras, err = sh.Format(vt.FormatOptions{Region: vt.RegionNone, Extras: cutExtras}); err != nil {
		return nil, err
	}
	if c.title, err = sh.Title(); err != nil {
		return nil, err
	}
	if c.pwd, err = sh.Pwd(); err != nil {
		return nil, err
	}
	if c.cursor, err = sh.CursorStyle(); err != nil {
		return nil, err
	}
	return c, nil
}

// entered records what the enter sequence changed, so the leave undoes
// exactly that.
type entered struct {
	kitty        bool // pushed kitty keyboard flags 0
	promptScreen bool // switched to the alternate screen with 1047
}

func enterFor(c *cutState) entered { return entered{kitty: true, promptScreen: !c.alt} }

// enterSeq changes only set D: the prompt's screen when the app is on the
// primary one, kitty keyboard, the drawing modes, the scroll region and
// the pen. It clears the screen the prompt draws on in both cases:
// Bubble Tea's renderer assumes a clear screen.
func enterSeq(e entered) string {
	var s strings.Builder
	if e.promptScreen {
		// 1047, not 1049: 1049 saves the cursor into the app's DECSC slot.
		s.WriteString("\x1b[?1047h")
	}
	if e.kitty {
		s.WriteString("\x1b[>0u")
	}
	s.WriteString("\x1b[4l\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7h\x1b[?25h\x1b[?2026l")
	s.WriteString(penReset)
	s.WriteString(clearScreen)
	return s.String()
}

// leaveScreen pops the prompt's kitty flags and leaves its screen.
func leaveScreen(e entered) string {
	var s string
	if e.kitty {
		s += "\x1b[<u"
	}
	if e.promptScreen {
		s += "\x1b[?1047l"
	}
	return s
}

// rawLeave restores the real terminal to the cut and replays the output
// logged since: leave the prompt's screen, set-D modes except IRM, the pen
// reset and the cut extras, IRM, the log. DECOM and DECSTBM come before
// the cursor, because both home it; IRM comes after it, because the
// pending-wrap reprint must not insert. With nothing entered it undoes
// T1's writes, which is the leave after a barrier timeout.
func rawLeave(c *cutState, e entered, log []byte) []byte {
	var b bytes.Buffer
	b.WriteString(leaveScreen(e))
	for _, k := range drawModes {
		b.WriteString(modeSeq(k, c.modes[k]))
	}
	b.WriteString(penReset)
	b.Write(c.extras)
	b.WriteString(modeSeq(modeIRM, c.modes[modeIRM]))
	b.Write(log)
	return b.Bytes()
}

// snapshotExtras are every extra except modes: the snapshot writes each
// mode itself, and the formatter's would enable report modes again.
var snapshotExtras = func() vt.Extras {
	x := vt.AllExtras
	x.Modes = false
	return x
}()

// snapshotLeave rebuilds the real terminal from the shadow without
// assuming anything about it beyond the cut. The order follows
// snapshotLeave in vt/internal/ghostty/restore_test.go: leave the prompt's
// screen, match the screen, reconcile every mode, reset the pen, clear,
// the formatter output. Then come the title, working directory and cursor
// style, which the formatter doesn't emit, and the continuation of an
// unfinished sequence. resync reports that the continuation was
// unavailable, so the caller must drop output up to the next ground.
func snapshotLeave(sh *vt.Terminal, c *cutState, e entered) (out []byte, resync bool, err error) {
	// Synchronized output off, so the real terminal draws the snapshot.
	if err := sh.SetMode(2026, false, false); err != nil {
		return nil, false, err
	}
	alt, err := sh.AltScreen()
	if err != nil {
		return nil, false, err
	}
	modes, err := sh.Modes()
	if err != nil {
		return nil, false, err
	}
	screen, err := sh.Format(vt.FormatOptions{Unwrap: true, Extras: snapshotExtras, Region: vt.RegionScreen})
	if err != nil {
		return nil, false, err
	}
	title, err := sh.Title()
	if err != nil {
		return nil, false, err
	}
	pwd, err := sh.Pwd()
	if err != nil {
		return nil, false, err
	}
	cursor, err := sh.CursorStyle()
	if err != nil {
		return nil, false, err
	}
	cont, err := sh.Continuation()
	switch {
	case errors.Is(err, vt.ErrContinuationUnavailable):
		resync = true
	case err != nil:
		return nil, false, err
	}
	current := make(map[modeKey]bool, len(modes))
	for _, m := range modes {
		current[modeKey{m.Mode, m.ANSI}] = m.Value
	}

	var b bytes.Buffer
	b.WriteString(leaveScreen(e))
	// The real terminal is on the cut's screen now.
	switch {
	case c.alt && !alt:
		b.WriteString(screenSeq(c.modes, false))
	case !c.alt && alt:
		// Enter before the clear, so the primary screen keeps its content.
		// 1049 saves the cursor and pen: put back the cut's first, not the
		// prompt's.
		b.Write(rawLeave(c, entered{}, nil))
		b.WriteString(screenSeq(current, true))
	}
	for _, m := range modes {
		k := modeKey{m.Mode, m.ANSI}
		if !m.ANSI && skipReconcile[m.Mode] {
			continue
		}
		if !m.ANSI && reportModes[m.Mode] && m.Value == c.modes[k] {
			continue
		}
		b.WriteString(modeSeq(k, m.Value))
	}
	b.WriteString(penReset)
	b.WriteString(keyboardReset)
	b.WriteString(clearScreen)
	b.Write(screen)
	// After a forced cut the real terminal's title and directory are
	// unknown: some terminals, ghostty among them, apply an OSC that CAN
	// ends instead of dropping it.
	if title != c.title || c.forced {
		fmt.Fprintf(&b, "\x1b]2;%s\x1b\\", oscText(title))
	}
	if pwd != c.pwd || c.forced {
		fmt.Fprintf(&b, "\x1b]7;%s\x1b\\", oscText(pwd))
	}
	if cursor != c.cursor {
		b.WriteString(cursor.DECSCUSR())
	}
	b.Write(cont)
	return b.Bytes(), resync, nil
}

// resetLeave is the last resort when neither replay nor snapshot is
// possible: RIS, after which the caller nudges the app to repaint.
func resetLeave(e entered) []byte { return []byte(leaveScreen(e) + ris) }

// screenSeq switches the alternate screen on or off with the mode that is
// set in modes, preferring 1049, so no mode bit stays set.
func screenSeq(modes map[modeKey]bool, on bool) string {
	n := uint16(47)
	switch {
	case modes[modeKey{1049, false}]:
		n = 1049
	case modes[modeKey{1047, false}]:
		n = 1047
	}
	return modeSeq(modeKey{n, false}, on)
}

// modeSeq is the SM or RM sequence that sets k to on.
func modeSeq(k modeKey, on bool) string {
	prefix, final := "?", "l"
	if k.ansi {
		prefix = ""
	}
	if on {
		final = "h"
	}
	return fmt.Sprintf("\x1b[%s%d%s", prefix, k.mode, final)
}

// oscText drops controls from an OSC payload, so it can't end the sequence
// early.
func oscText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./overlay/ -v -run 'TestCaptureCut|TestEnterSeq|TestRawLeave|TestSnapshotLeave|TestLeaveKeeps|TestResetLeave|TestOSCText'`
Expected: PASS. A failing `TestSnapshotLeaveRestoresChangesWhileDetached` case is a real restore bug: diff the two `termState` values, compare the snapshot output with `snapshotLeave` in `vt/internal/ghostty/restore_test.go`, and fix the leave, not the test. In particular, if "modifyOtherKeys reset" fails, check how libghostty parses `CSI >4;0m` with `vt/internal/ghostty` before changing `keyboardReset`.

- [ ] **Step 6: Commit**

```bash
gofmt -l overlay
git add overlay/restore.go overlay/restore_test.go overlay/helpers_test.go
git commit -m "Add the overlay's cut capture and enter/leave sequences

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: `Terminal` core: output pump, answers, resize, and the test harness

**Files:**
- Create: `overlay/terminal.go`
- Test: `overlay/harness_test.go`, `overlay/terminal_test.go`

**Interfaces:**
- Consumes: `InputMux` (Task 5), `snapshotLeave`, `cutState`, `entered` (Task 7), `vt.NewTerminal` and options.
- Produces: `type overlay.Config struct{ Stdin io.Reader; Stdout io.Writer; ContainerIn io.Writer; ContainerOut io.Reader; CloseInput func() error; Resize func(cols, rows int); Size func() (cols, rows int, err error); Environ []string; Logf func(format string, args ...any); NoShadow bool }`; `type overlay.Terminal`; `func overlay.New(cfg Config) *Terminal`; `func (*Terminal) Run(ctx context.Context) error`; `func (*Terminal) Resize()`. Unexported, used by Tasks 9 and 10: `type timing`, `var defaultTiming`, `type detachReq`, `type rawLog` (`append(p []byte, max int)`), `type lockedWriter`, the `Terminal` fields, `(*Terminal).output`, `shadowWriteLocked`, `failLocked`, `flushAnswersLocked`, `size`, `isDone`, `func isClosed(<-chan struct{}) bool`, `const barrierTimeoutsBeforeDegraded = 2`, `const shadowContinuationBytes`. Test harness (`harness_test.go`): `newHarness(t, cols, rows, ...harnessOption) *harness`, options `silentTerminal()`, `withoutShadow()`, `withTiming(func(*timing))`, methods `app`, `keys`, `resize`, `resizeLog`, `waitContainer`, `waitScreen`, `show`, `result`, `locked`, fields `term`, `real`, `stdin`, `out`, `toCont`, `stdout`, `closedIn`, `done`, `err`; `firstLine(term) string`.

- [ ] **Step 1: Write the harness**

`overlay/harness_test.go`:

```go
package overlay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/require"
)

// testTiming keeps budgets long where a test must not hit them. A test
// that waits for a budget shortens it with withTiming.
var testTiming = timing{
	groundWait:  5 * time.Second,
	groundBytes: 64 << 10,
	barrierWait: 5 * time.Second,
	silence:     30 * time.Millisecond,
	lateStrip:   5 * time.Second,
	nudgeGap:    time.Millisecond,
	logMax:      4 << 20,
}

type harnessConfig struct {
	silent   bool
	noShadow bool
	timing   timing
}

type harnessOption func(*harnessConfig)

// silentTerminal makes the stand-in answer no queries, like a terminal
// without DSR 5n.
func silentTerminal() harnessOption { return func(c *harnessConfig) { c.silent = true } }

func withoutShadow() harnessOption { return func(c *harnessConfig) { c.noShadow = true } }

func withTiming(f func(*timing)) harnessOption { return func(c *harnessConfig) { f(&c.timing) } }

// harness runs a Terminal against fakes. real stands in for the user's
// terminal: it gets everything written to stdout and answers queries on
// stdin, in order, like a real terminal.
type harness struct {
	t        *testing.T
	term     *Terminal
	real     *vt.Terminal
	stdin    *bufPipe
	out      *chunkReader // the container's output
	toCont   *syncBuffer  // the container's input
	stdout   *syncBuffer
	closedIn atomic.Int32
	done     chan struct{} // closed when Run returned
	err      error         // Run's result, after done

	mu         sync.Mutex
	cols, rows int
	resizes    []string
}

func newHarness(t *testing.T, cols, rows int, opts ...harnessOption) *harness {
	t.Helper()
	hc := harnessConfig{timing: testTiming}
	for _, o := range opts {
		o(&hc)
	}
	h := &harness{
		t:      t,
		stdin:  newBufPipe(1 << 20),
		out:    newChunkReader(),
		toCont: &syncBuffer{},
		stdout: &syncBuffer{},
		done:   make(chan struct{}),
		cols:   cols,
		rows:   rows,
	}
	var vtOpts []vt.TerminalOption
	if !hc.silent {
		vtOpts = append(vtOpts, vt.WithWritePty(func(b []byte) { _, _ = h.stdin.Write(b) }))
	}
	h.real = newVT(t, cols, rows, vtOpts...)
	h.term = New(Config{
		Stdin:        h.stdin,
		Stdout:       stdoutWriter{h},
		ContainerIn:  h.toCont,
		ContainerOut: h.out,
		CloseInput:   func() error { h.closedIn.Add(1); return nil },
		Resize:       h.recordResize,
		Size:         h.size,
		Environ:      []string{"TERM=xterm-256color"},
		NoShadow:     hc.noShadow,
	})
	h.term.timing = hc.timing
	// stdoutWriter isn't a TTY, so detection would find no colours.
	h.term.profile = colorprofile.ANSI256
	go func() {
		h.err = h.term.Run(context.Background())
		close(h.done)
	}()
	t.Cleanup(h.stop)
	return h
}

// stdoutWriter records what reaches the real terminal and feeds the
// stand-in.
type stdoutWriter struct{ h *harness }

func (w stdoutWriter) Write(p []byte) (int, error) {
	_, _ = w.h.stdout.Write(p)
	return w.h.real.Write(p)
}

func (h *harness) stop() {
	h.out.close()
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Error("Run didn't return")
	}
}

func (h *harness) size() (int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cols, h.rows, nil
}

func (h *harness) recordResize(cols, rows int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resizes = append(h.resizes, fmt.Sprintf("%dx%d", cols, rows))
}

func (h *harness) resizeLog() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.resizes...)
}

// resize changes the local terminal's size, as SIGWINCH would.
func (h *harness) resize(cols, rows int) {
	h.t.Helper()
	h.mu.Lock()
	h.cols, h.rows = cols, rows
	h.mu.Unlock()
	require.NoError(h.t, h.real.Resize(uint16(cols), uint16(rows)))
	h.term.Resize()
}

// app writes container output and waits until the pump handled it.
func (h *harness) app(s string) {
	h.t.Helper()
	h.out.send(h.t, s)
}

// keys types on the local terminal.
func (h *harness) keys(s string) { _, _ = h.stdin.Write([]byte(s)) }

// waitContainer waits until the container's input is exactly want.
func (h *harness) waitContainer(want string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.toCont.String() == want }, 5*time.Second, time.Millisecond,
		"the container's input never became %q", want)
}

// waitScreen waits until the real terminal's screen contains s.
func (h *harness) waitScreen(s string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return strings.Contains(screenText(h.real), s) }, 10*time.Second, 5*time.Millisecond,
		"the screen never showed %q", s)
}

// show runs Show in the background.
func (h *harness) show(ctx context.Context, model tea.Model) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.term.Show(ctx, model) }()
	return ch
}

// result waits for a background call's error.
func (h *harness) result(ch <-chan error) error {
	h.t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		h.t.Fatal("the call didn't return")
		return nil
	}
}

// locked runs f under the terminal's lock, for reading its state.
func (h *harness) locked(f func(t *Terminal)) {
	h.term.mu.Lock()
	defer h.term.mu.Unlock()
	f(h.term)
}

func firstLine(term *vt.Terminal) string {
	return strings.SplitN(screenText(term), "\n", 2)[0]
}
```

- [ ] **Step 2: Write the failing tests**

`overlay/terminal_test.go`:

```go
package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughIsByteExact(t *testing.T) {
	h := newHarness(t, 20, 5)
	chunks := []string{"plain ", "\x1b[3", "1mred\x1b[0m ", "\xe2\x82", "\xac\r\n", "\x1b]2;title\x07"}
	for _, c := range chunks {
		h.app(c)
	}
	assert.Equal(t, strings.Join(chunks, ""), h.stdout.String())
	h.keys("ls\r\x1b[A")
	h.waitContainer("ls\r\x1b[A")
}

func TestShadowAnswersAreDroppedWhileAttached(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b[6n")
	h.waitContainer("\x1b[1;1R")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "only the real terminal answers")
}

func TestWithoutShadow(t *testing.T) {
	h := newHarness(t, 20, 5, withoutShadow())
	h.app("abc")
	assert.Equal(t, "abc", h.stdout.String())
	assert.Nil(t, h.term.shadow)
}

func TestResize(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.resize(30, 6)
	assert.Equal(t, []string{"30x6"}, h.resizeLog())
	h.app(strings.Repeat("x", 25))
	assert.Equal(t, strings.Repeat("x", 25), firstLine(h.term.shadow), "the shadow has the new width")
	h.term.Resize()
	assert.Equal(t, []string{"30x6", "30x6"}, h.resizeLog(), "the container gets every resize")
}

func TestRunEndsWithTheOutput(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.out.close()
	select {
	case <-h.done:
		assert.NoError(t, h.err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return")
	}
	assert.Zero(t, h.closedIn.Load(), "stdin is still open")
}

func TestStdinEOFClosesTheContainerInput(t *testing.T) {
	h := newHarness(t, 20, 5)
	require.NoError(t, h.stdin.Close())
	require.Eventually(t, func() bool { return h.closedIn.Load() == 1 }, 5*time.Second, time.Millisecond)
	h.app("bye")
	assert.Equal(t, "bye", h.stdout.String(), "output flows until it ends")
	select {
	case <-h.done:
		t.Fatal("Run returned before the output ended")
	default:
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./overlay/ -run 'TestPassthrough|TestShadowAnswers|TestWithoutShadow|TestResize|TestRunEnds|TestStdinEOF'`
Expected: FAIL to compile: `undefined: New`, `undefined: Config`, `undefined: timing`.

- [ ] **Step 4: Write the terminal core**

`overlay/terminal.go`:

```go
package overlay

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/colorprofile"
)

// Shadow configuration on the host. The snapshot uses only the visible
// screen, and 64 KiB of continuation covers OSC 52 clipboard writes and
// kitty graphics.
const (
	shadowScrollbackLines   = 1000
	shadowContinuationBytes = 64 << 10
)

// barrierTimeoutsBeforeDegraded consecutive barrier timeouts switch T2 to
// waiting for input silence.
const barrierTimeoutsBeforeDegraded = 2

// Config connects a Terminal to the local terminal and the container.
type Config struct {
	Stdin        io.Reader // the local terminal, already in raw mode
	Stdout       io.Writer
	ContainerIn  io.Writer
	ContainerOut io.Reader
	// CloseInput half-closes the container's input after stdin ends.
	CloseInput func() error
	// Resize resizes the container's PTY.
	Resize func(cols, rows int)
	// Size reports the local terminal's size.
	Size func() (cols, rows int, err error)
	// Environ is the prompt program's environment; nil means os.Environ.
	// Its TERM picks the sequences Bubble Tea's renderer uses.
	Environ []string
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)
	// NoShadow skips the emulator for sessions that never prompt. Output
	// passes through, and Show returns ErrUnavailable.
	NoShadow bool
}

// timing holds the spec's budgets; tests shorten them.
type timing struct {
	groundWait  time.Duration // T1: longest wait for ground before a forced cut
	groundBytes int           // T1: most bytes forwarded while waiting for ground
	barrierWait time.Duration // T2: wait for the barrier reply
	silence     time.Duration // T2 without barrier support: input quiet time
	lateStrip   time.Duration // after a barrier timeout: drop a late reply
	nudgeGap    time.Duration // between the two resizes of a repaint nudge
	logMax      int           // raw log cap
}

var defaultTiming = timing{
	groundWait:  250 * time.Millisecond,
	groundBytes: 64 << 10,
	barrierWait: 500 * time.Millisecond,
	silence:     100 * time.Millisecond,
	lateStrip:   10 * time.Second,
	nudgeGap:    100 * time.Millisecond,
	logMax:      4 << 20,
}

var errNoShadow = errors.New("no shadow terminal")

// Terminal forwards a container session to the local terminal and shows
// prompts over it. Create it with New and run it with Run.
type Terminal struct {
	cfg     Config
	timing  timing
	in      *InputMux
	toCont  *lockedWriter
	environ []string
	profile colorprofile.Profile
	logf    func(format string, args ...any)
	done    chan struct{} // closed when Run returns

	showMu sync.Mutex // one Show at a time; finish takes it to wait for one

	mu                 sync.Mutex // guards everything below
	shadow             *vt.Terminal
	shadowErr          error // set: prompts are unavailable
	cols, rows         int
	attached           bool
	detach             *detachReq // a T1 waiting for ground
	cut                *cutState  // from T1 to T3
	log                rawLog     // output since the cut
	answers            []byte     // the shadow's answers during the current call
	answered           bool       // the shadow answered a query while detached
	resized            bool       // resized while detached
	resyncing          bool       // drop output up to the next ground
	prog               *tea.Program
	closed             bool
	barrierTimeouts    int
	barrierUnsupported bool
	// snapshot is snapshotLeave; tests replace it to inject failures.
	snapshot func(*vt.Terminal, *cutState, entered) ([]byte, bool, error)
}

// detachReq is a pending T1: the pump cuts at the next byte where the
// shadow is at ground.
type detachReq struct {
	done      chan struct{}
	forwarded int   // bytes forwarded while waiting for ground
	err       error // why no cut happened
}

// rawLog holds the container output since the cut, for raw replay.
type rawLog struct {
	buf      []byte
	overflow bool
}

// append keeps p unless the log would pass max. Past it the log is dropped,
// which rules out raw replay; the pump never waits.
func (l *rawLog) append(p []byte, max int) {
	if l.overflow {
		return
	}
	if len(l.buf)+len(p) > max {
		l.buf, l.overflow = nil, true
		return
	}
	l.buf = append(l.buf, p...)
}

// lockedWriter serializes the two writers of the container's input: the
// stdin pump and the shadow's answers.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// New sets up a Terminal. A shadow that can't be created only turns
// prompts off.
func New(cfg Config) *Terminal {
	t := &Terminal{
		cfg:      cfg,
		timing:   defaultTiming,
		done:     make(chan struct{}),
		environ:  cfg.Environ,
		logf:     cfg.Logf,
		attached: true,
		snapshot: snapshotLeave,
	}
	if t.environ == nil {
		t.environ = os.Environ()
	}
	if t.logf == nil {
		t.logf = func(string, ...any) {}
	}
	t.profile = colorprofile.Detect(cfg.Stdout, t.environ)
	t.toCont = &lockedWriter{w: cfg.ContainerIn}
	t.in = NewInputMux(cfg.Stdin, t.toCont)
	t.cols, t.rows = t.size()
	if cfg.NoShadow {
		t.shadowErr = errNoShadow
		return t
	}
	sh, err := vt.NewTerminal(uint16(t.cols), uint16(t.rows),
		vt.WithScrollbackLines(shadowScrollbackLines),
		vt.WithContinuationMaxBytes(shadowContinuationBytes),
		// Runs inside shadow calls, which all hold t.mu.
		vt.WithWritePty(func(b []byte) { t.answers = append(t.answers, b...) }),
	)
	if err != nil {
		t.shadowErr = err
		t.logf("overlay: no shadow terminal, prompts disabled: %v", err)
		return t
	}
	t.shadow = sh
	return t
}

// Run forwards stdin and the container's output until the output ends or
// ctx is done. When stdin ends it half-closes the container's input and
// keeps forwarding output.
func (t *Terminal) Run(ctx context.Context) error {
	defer t.finish()
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		if err := t.in.Run(); err != nil {
			t.logf("overlay: stdin: %v", err)
		}
		if t.cfg.CloseInput != nil {
			_ = t.cfg.CloseInput()
		}
	}()
	outDone := make(chan error, 1)
	go func() { outDone <- t.pump() }()
	select {
	case err := <-outDone:
		return err
	case <-inDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-outDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finish ends the session for Show. A prompt that is showing is cancelled
// and restores the screen before Run returns.
func (t *Terminal) finish() {
	close(t.done)
	t.showMu.Lock()
	defer t.showMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.shadow != nil {
		_ = t.shadow.Close()
	}
}

func (t *Terminal) pump() error {
	buf := make([]byte, 32<<10)
	for {
		n, err := t.cfg.ContainerOut.Read(buf)
		if n > 0 {
			t.output(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// output handles one chunk of container output.
func (t *Terminal) output(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.shadowWriteLocked(p)
	if t.attached {
		_, _ = t.cfg.Stdout.Write(p)
	}
}

func (t *Terminal) shadowWriteLocked(p []byte) {
	if t.shadowErr != nil || len(p) == 0 {
		return
	}
	if _, err := t.shadow.Write(p); err != nil {
		t.failLocked(err)
	}
	t.flushAnswersLocked()
}

// failLocked turns prompts off after the shadow failed. Output keeps
// passing through.
func (t *Terminal) failLocked(err error) {
	if t.shadowErr != nil {
		return
	}
	t.shadowErr = err
	if !t.closed {
		t.logf("overlay: shadow terminal failed, prompts disabled: %v", err)
	}
}

// flushAnswersLocked sends the shadow's answers to the container while
// detached. While attached the real terminal answers the same queries, so
// the shadow's answers are dropped.
func (t *Terminal) flushAnswersLocked() {
	if len(t.answers) == 0 {
		return
	}
	if !t.attached {
		_, _ = t.toCont.Write(t.answers)
		t.answered = true
	}
	t.answers = t.answers[:0]
}

// Resize applies the local terminal's size: to the shadow first, so its
// layout matches what the app redraws for, then to the container, then to
// a prompt that is showing.
func (t *Terminal) Resize() {
	cols, rows := t.size()
	t.mu.Lock()
	changed := cols != t.cols || rows != t.rows
	t.cols, t.rows = cols, rows
	if changed && t.shadowErr == nil {
		if err := t.shadow.Resize(uint16(cols), uint16(rows)); err != nil {
			t.failLocked(err)
		}
		t.flushAnswersLocked()
	}
	if changed && !t.attached {
		// The log was written for the old geometry.
		t.resized = true
	}
	prog := t.prog
	t.mu.Unlock()
	if t.cfg.Resize != nil {
		t.cfg.Resize(cols, rows)
	}
	if prog != nil {
		// Bubble Tea can't see SIGWINCH: its output isn't a TTY.
		go prog.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
	}
}

// size is the local terminal's size, or 80x24 when unknown.
func (t *Terminal) size() (cols, rows int) {
	cols, rows = 80, 24
	if t.cfg.Size != nil {
		if c, r, err := t.cfg.Size(); err == nil && c > 0 && r > 0 {
			cols, rows = c, r
		}
	}
	return min(cols, math.MaxUint16), min(rows, math.MaxUint16)
}

func (t *Terminal) isDone() bool { return isClosed(t.done) }

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
```

- [ ] **Step 5: Run the tests, also under the race detector**

Run: `go test ./overlay/ && CGO_ENABLED=1 go test -race ./overlay/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l overlay
git add overlay/terminal.go overlay/terminal_test.go overlay/harness_test.go
git commit -m "Add overlay.Terminal: the output pump behind a shadow terminal

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: T1 at ground, the leave paths, resync

**Files:**
- Create: `overlay/detach.go`
- Modify: `overlay/terminal.go` (replace `output`)
- Test: `overlay/detach_test.go`

**Interfaces:**
- Consumes: Task 8's `Terminal` fields and helpers; Task 7's `captureCut`, `enterFor`, `rawLeave`, `resetLeave`, `keyboardReset`, `ris`; Task 5's `InputMux.Drain` and `Release`.
- Produces: `func (*Terminal) detachAt(ctx context.Context) error` (T1; nil means detached and the caller must call `leave`); `func (*Terminal) leave(drawn bool)` (T3); `leaveBytesLocked`, `cutLocked`, `advanceDetachLocked`, `resyncLocked`, `endDetachLocked`, `nudge`. Test helpers: `(*harness).detach()`, `detachAsync(ctx) <-chan error`, `waitDetachPending()`, `snapshotted() bool`.

- [ ] **Step 1: Write the failing tests**

`overlay/detach_test.go`:

```go
package overlay

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (h *harness) detach() {
	h.t.Helper()
	require.NoError(h.t, h.term.detachAt(context.Background()))
}

func (h *harness) detachAsync(ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.term.detachAt(ctx) }()
	return ch
}

func (h *harness) waitDetachPending() {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		var pending bool
		h.locked(func(t *Terminal) { pending = t.detach != nil })
		return pending
	}, 5*time.Second, time.Millisecond)
}

// snapshotted reports whether a leave was a snapshot: only the snapshot
// resets the keyboard state.
func (h *harness) snapshotted() bool { return strings.Contains(h.stdout.String(), keyboardReset) }

func TestDetachCutsAtGround(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("abc\xe2")
	done := h.detachAsync(context.Background())
	h.waitDetachPending()
	h.app("\x82\xacX")
	require.NoError(t, h.result(done))
	assert.Equal(t, "abc\xe2\x82\xac"+barrierQuery, h.stdout.String(), "forwarding stops right after the character")
	h.term.leave(false)
	assert.False(t, h.snapshotted())
	assert.Equal(t, "abc€X", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestDetachAtGroundIsImmediate(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("ab")
	h.detach()
	assert.Equal(t, "ab"+barrierQuery, h.stdout.String())
	h.locked(func(tm *Terminal) {
		assert.False(t, tm.attached)
		assert.False(t, tm.cut.forced)
		assert.True(t, tm.cut.barrierSent)
	})
	h.app("cd")
	assert.Equal(t, "ab"+barrierQuery, h.stdout.String(), "output is logged, not forwarded")
	h.term.leave(false)
	assert.Equal(t, "abcd", firstLine(h.real))
}

func TestForcedCutAfterTheByteBudget(t *testing.T) {
	h := newHarness(t, 40, 5, withTiming(func(tm *timing) { tm.groundBytes = 16 }))
	h.app("\x1b]2;")
	done := h.detachAsync(context.Background())
	h.waitDetachPending()
	h.app(strings.Repeat("y", 20))
	require.NoError(t, h.result(done))
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x18"+barrierQuery), "CAN aborts the sequence on the real terminal")
	h.app("\x07after")
	h.term.leave(false)
	assert.True(t, h.snapshotted(), "a forced cut rules out raw replay")
	assert.Equal(t, "after", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestForcedCutAfterTheTimeBudget(t *testing.T) {
	h := newHarness(t, 20, 5, withTiming(func(tm *timing) { tm.groundWait = 20 * time.Millisecond }))
	h.app("\x1b]2;stall")
	start := time.Now()
	h.detach()
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	h.locked(func(tm *Terminal) { assert.True(t, tm.cut.forced) })
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x18"+barrierQuery))
	h.term.leave(false)
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestCutTurnsSynchronizedOutputOff(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b[?2026hframe")
	h.detach()
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x1b[?2026l"+barrierQuery))
	h.app(" more")
	h.term.leave(false)
	assert.False(t, h.snapshotted())
	on, err := h.real.Mode(2026, false)
	require.NoError(t, err)
	assert.True(t, on, "raw replay restores the app's mode")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShadowAnswersGoToTheContainerWhileDetached(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("\x1b[6n")
	h.waitContainer("\x1b[1;1R")
	h.term.leave(false)
	assert.True(t, h.snapshotted(), "a replayed query would be answered twice")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "answered exactly once")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestLogOverflowForcesASnapshot(t *testing.T) {
	h := newHarness(t, 20, 5, withTiming(func(tm *timing) { tm.logMax = 8 }))
	h.detach()
	h.app("0123456789")
	h.term.leave(false)
	assert.True(t, h.snapshotted())
	assert.Equal(t, "0123456789", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestResizeWhileDetachedForcesASnapshot(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("before")
	h.detach()
	h.resize(24, 6)
	h.app(" after")
	h.term.leave(false)
	assert.True(t, h.snapshotted())
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestSameSizeResizeKeepsRawReplay(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.term.Resize()
	h.term.leave(false)
	assert.False(t, h.snapshotted())
}

func TestResyncWhenTheContinuationIsUnavailable(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("a")
	h.detach()
	h.app("\x1b]2;" + strings.Repeat("y", shadowContinuationBytes+1024))
	h.resize(24, 6) // rules out raw replay
	h.term.leave(false)
	h.locked(func(tm *Terminal) { assert.True(t, tm.resyncing) })
	h.app("yyy\x07after")
	h.locked(func(tm *Terminal) { assert.False(t, tm.resyncing) })
	assert.Equal(t, "aafter", firstLine(h.real), "the tail of the sequence doesn't show as text")
}

func TestSnapshotCompletesAnUnfinishedSequence(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("abc\x1b[3")
	h.resize(24, 6)
	h.term.leave(false)
	h.app("1mRED\x1b[0m")
	assert.Equal(t, "abcRED", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShadowFailureLeavesWithAReset(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.resize(24, 6) // rules out raw replay
	h.locked(func(tm *Terminal) { tm.shadowErr = errors.New("trap") })
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), ris))
	require.Eventually(t, func() bool {
		return slices.Equal(h.resizeLog(), []string{"24x6", "24x5", "24x6"})
	}, 5*time.Second, time.Millisecond, "a size change makes the app repaint")
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrUnavailable)
}

func TestShadowFailureStillReplaysRaw(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("x")
	h.locked(func(tm *Terminal) { tm.shadowErr = errors.New("trap") })
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), "x"), "raw replay doesn't need the shadow")
}

func TestSnapshotOutOfMemoryResetsWithoutFailingTheShadow(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.locked(func(tm *Terminal) {
		tm.snapshot = func(*vt.Terminal, *cutState, entered) ([]byte, bool, error) {
			return nil, false, fmt.Errorf("format: %w", vt.ErrOutOfMemory)
		}
	})
	h.detach()
	h.resize(24, 6)
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), ris))
	h.locked(func(tm *Terminal) { assert.NoError(t, tm.shadowErr) })
	h.detach()
	h.term.leave(false)
}

func TestDetachCancelledBeforeGround(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b]2;x")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.detachAsync(ctx)
	h.waitDetachPending()
	cancel()
	assert.ErrorIs(t, h.result(done), context.Canceled)
	assert.Equal(t, "\x1b]2;x", h.stdout.String(), "nothing written")
	h.app("\x07ok")
	assert.Equal(t, "\x1b]2;x\x07ok", h.stdout.String(), "still attached")
}

func TestDetachUnavailableWithoutShadow(t *testing.T) {
	h := newHarness(t, 20, 5, withoutShadow())
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrUnavailable)
}

func TestDetachAfterStdinEOF(t *testing.T) {
	h := newHarness(t, 20, 5)
	require.NoError(t, h.stdin.Close())
	<-h.term.in.Done()
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrClosed)
}

func TestLateBarrierReplyIsStripped(t *testing.T) {
	h := newHarness(t, 20, 5, silentTerminal())
	h.detach()
	h.term.leave(false)
	h.keys("a\x1b[0nb")
	h.waitContainer("ab")
	h.keys("\x1b[0n")
	h.waitContainer("ab\x1b[0n")
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./overlay/ -run 'TestDetach|TestForcedCut|TestCut|TestShadowAnswersGo|TestLogOverflow|TestResizeWhileDetached|TestSameSize|TestResync|TestSnapshotCompletes|TestShadowFailure|TestSnapshotOutOfMemory|TestLateBarrier'`
Expected: FAIL to compile: `h.term.detachAt undefined`, `h.term.leave undefined`.

- [ ] **Step 3: Write T1 and T3**

`overlay/detach.go`:

```go
package overlay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bernd/vibepit/vt"
)

// can aborts the escape sequence a terminal is in the middle of.
const can = 0x18

// detachAt is T1: it stops forwarding output at a byte where the shadow's
// parser is at ground, or after the budget with a forced cut. Without an
// error the terminal is detached, and the caller must end with leave.
func (t *Terminal) detachAt(ctx context.Context) error {
	if isClosed(t.in.Done()) {
		return ErrClosed
	}
	t.mu.Lock()
	switch {
	case t.closed:
		t.mu.Unlock()
		return ErrClosed
	case t.shadowErr != nil:
		err := t.shadowErr
		t.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req := &detachReq{done: make(chan struct{})}
	t.detach = req
	switch ground, err := t.shadow.AtGround(); {
	case err != nil:
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
	case ground:
		t.cutLocked(false)
	}
	t.mu.Unlock()

	timer := time.NewTimer(t.timing.groundWait)
	defer timer.Stop()
	var stop error
	select {
	case <-req.done:
	case <-timer.C:
	case <-ctx.Done():
		stop = ctx.Err()
	case <-t.done:
		stop = ErrClosed
	}
	t.mu.Lock()
	if t.detach == req {
		if stop != nil {
			t.endDetachLocked(stop)
		} else {
			// No ground within the time budget, e.g. a stalled OSC.
			t.cutLocked(true)
		}
	}
	t.mu.Unlock()
	<-req.done
	return req.err
}

// endDetachLocked finishes the pending T1; err says why no cut happened.
func (t *Terminal) endDetachLocked(err error) {
	t.detach.err = err
	close(t.detach.done)
	t.detach = nil
}

// cutLocked is T1 at the current byte: capture the cut, stop forwarding,
// and send the barrier query. After a forced cut, CAN aborts the sequence
// the real terminal is in the middle of.
func (t *Terminal) cutLocked(forced bool) {
	c, err := captureCut(t.shadow)
	if err != nil {
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
		return
	}
	c.forced = forced
	c.barrierSent = !t.barrierUnsupported
	t.cut = c
	t.attached = false
	t.log = rawLog{}
	t.answered, t.resized = false, false
	// Drain before the query goes out: a local terminal can answer before
	// the next statement runs.
	t.in.Drain()
	var b []byte
	if forced {
		b = append(b, can)
	}
	if c.modes[modeSync] {
		// The real terminal must not hold back drawing during the prompt.
		b = append(b, "\x1b[?2026l"...)
	}
	if c.barrierSent {
		b = append(b, barrierQuery...)
	}
	_, _ = t.cfg.Stdout.Write(b)
	t.endDetachLocked(nil)
}

// advanceDetachLocked forwards p up to the byte where the shadow reaches
// ground, cuts there, and returns the rest for the shadow and the log.
func (t *Terminal) advanceDetachLocked(p []byte) []byte {
	ground, err := t.shadow.AtGround()
	if err == nil && !ground {
		if len(p) == 0 {
			return p
		}
		var n int
		n, ground, err = t.shadow.WriteUntilGround(p)
		if err == nil {
			t.flushAnswersLocked()
			_, _ = t.cfg.Stdout.Write(p[:n])
			t.detach.forwarded += n
			p = p[n:]
		}
	}
	switch {
	case err != nil:
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
	case ground:
		t.cutLocked(false)
	case t.detach.forwarded >= t.timing.groundBytes:
		t.cutLocked(true)
	}
	return p
}

// resyncLocked drops output up to the next ground: the real terminal never
// got the start of the sequence the shadow is in.
func (t *Terminal) resyncLocked(p []byte) []byte {
	if len(p) == 0 {
		return p
	}
	ground, err := t.shadow.AtGround()
	if err == nil && !ground {
		var n int
		n, ground, err = t.shadow.WriteUntilGround(p)
		if err == nil {
			t.flushAnswersLocked()
			p = p[n:]
		}
	}
	if err != nil {
		t.failLocked(err)
		ground = true
	}
	if ground {
		t.resyncing = false
	}
	return p
}

// leave is T3. Holding t.mu, and the InputMux lock through Release, it
// writes the leave sequence, attaches output and hands input back, so no
// container output and no input slips in between. drawn tells whether the
// enter sequence was written.
func (t *Terminal) leave(drawn bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cut
	var e entered
	if drawn {
		e = enterFor(c)
	}
	var strip time.Duration
	if !drawn && c.barrierSent {
		// The barrier reply may still come; it must not reach the app.
		strip = t.timing.lateStrip
	}
	var nudge bool
	t.in.Release(strip, func() {
		out, resync, reset := t.leaveBytesLocked(c, e, drawn)
		_, _ = t.cfg.Stdout.Write(out)
		t.attached = true
		t.resyncing = resync
		nudge = reset
	})
	t.cut = nil
	t.log = rawLog{}
	if nudge {
		go t.nudge()
	}
}

// leaveBytesLocked picks the leave: raw replay when the log reproduces the
// screen exactly, else a snapshot from the shadow, else a reset.
func (t *Terminal) leaveBytesLocked(c *cutState, e entered, drawn bool) (out []byte, resync, reset bool) {
	raw := !c.forced && // the real terminal may show a replacement character
		!t.log.overflow && // bytes are missing
		!t.resized && // the log was written for another geometry
		!t.answered && // a replayed query would be answered twice
		!(drawn && c.alt) // the prompt drew over the app's alternate screen
	if raw {
		return rawLeave(c, e, t.log.buf), false, false
	}
	if t.shadowErr == nil {
		out, resync, err := t.snapshot(t.shadow, c, e)
		if err == nil {
			return out, resync, false
		}
		// A screen too large to format under the memory limit leaves the
		// shadow usable; anything else means it can't be trusted.
		if !errors.Is(err, vt.ErrOutOfMemory) {
			t.failLocked(err)
		}
		t.logf("overlay: snapshot restore failed, resetting the terminal: %v", err)
	}
	return resetLeave(e), false, true
}

// nudge makes the app repaint after a reset: a size change delivers
// SIGWINCH, which the kernel skips when the size stays the same.
func (t *Terminal) nudge() {
	if t.cfg.Resize == nil {
		return
	}
	t.mu.Lock()
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	if rows > 1 {
		t.cfg.Resize(cols, rows-1)
	} else {
		t.cfg.Resize(cols+1, rows)
	}
	time.Sleep(t.timing.nudgeGap)
	t.cfg.Resize(cols, rows)
}
```

- [ ] **Step 4: Route output through the detach and the resync**

In `overlay/terminal.go`, replace `output` with:

```go
// output handles one chunk of container output. Resync runs first: while
// it drops a sequence tail the shadow isn't at ground, so a pending detach
// can only cut after it.
func (t *Terminal) output(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resyncing && t.shadowErr == nil {
		p = t.resyncLocked(p)
	}
	if t.detach != nil {
		p = t.advanceDetachLocked(p)
	}
	t.shadowWriteLocked(p)
	switch {
	case len(p) == 0:
	case t.attached:
		_, _ = t.cfg.Stdout.Write(p)
	default:
		t.log.append(p, t.timing.logMax)
	}
}
```

- [ ] **Step 5: Run the tests, also under the race detector**

Run: `go test ./overlay/ && CGO_ENABLED=1 go test -race ./overlay/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l overlay
git add overlay/detach.go overlay/detach_test.go overlay/terminal.go
git commit -m "Detach the overlay at parser ground and restore on leave

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: `Show`: the barrier, the prompt program, T3

**Files:**
- Create: `overlay/show.go`
- Test: `overlay/show_test.go`

**Interfaces:**
- Consumes: `detachAt`, `leave` (Task 9); `InputMux.AwaitBarrier`, `AwaitSilence` (Task 5); `NewFilter` (Task 6); `enterSeq`, `enterFor` (Task 7).
- Produces: `func (*Terminal) Show(ctx context.Context, model tea.Model) error`. Errors: `ErrUnavailable` (wrapped with the cause), `ErrBarrierTimeout` (nothing drawn), `ErrClosed` (session ended), `ctx.Err()`, or the program's error (for a panic in the model, wrapping `tea.ErrProgramPanic`). Show waits for an earlier Show. The screen is restored on every path after T1.

- [ ] **Step 1: Write the failing tests**

`overlay/show_test.go`:

```go
package overlay

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyModel shows its text and width full-screen and quits on the first
// key.
type keyModel struct {
	text       string
	panicOnKey bool
	width      int
}

func (m keyModel) Init() tea.Cmd { return nil }

func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyPressMsg:
		if m.panicOnKey {
			panic("prompt failed")
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m keyModel) View() tea.View {
	v := tea.NewView(fmt.Sprintf("%s W=%d", m.text, m.width))
	v.AltScreen = true
	return v
}

var promptModel = keyModel{text: "PROMPT"}

// prompt shows promptModel, waits until it is drawn, and answers it with
// a key.
func (h *harness) prompt(during func()) {
	h.t.Helper()
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	if during != nil {
		during()
	}
	h.keys("x")
	require.NoError(h.t, h.result(done))
	assert.NotContains(h.t, screenText(h.real), "PROMPT", "the prompt is gone")
}

func TestShowRestoresThePrimaryScreenByReplay(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ls\r\nfile\r\n\x1b[2;5r\x1b[4h\x1b(0\x1b[31m\x1b[6;1H" + strings.Repeat("x", 20) + "\x1b7")
	h.prompt(func() { h.app("q\x1b[0m\r\nmore") })
	assert.False(t, h.snapshotted(), "raw replay")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowOnTheAlternateScreenRestoresFromTheShadow(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[2;3Hvim")
	h.prompt(func() { h.app("\x1b[3;1H~edited") })
	assert.True(t, h.snapshotted(), "the prompt drew over the app's screen")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowChangesOnlySetD(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("\x1b[?2004h\x1b[?1004h\x1b[?1000h\x1b[>1u\x1b]2;app\x07\x1b]7;file://h/p\x07\x1b[3 qtext")
	before := stateOf(t, h.real)
	setD := map[modeKey]bool{
		modeIRM: true, {20, true}: true, {5, false}: true, {6, false}: true,
		{7, false}: true, {25, false}: true, modeSync: true, {1047, false}: true,
	}
	h.prompt(func() {
		during := stateOf(t, h.real)
		for i, m := range during.Modes {
			if !setD[modeKey{m.Mode, m.ANSI}] {
				assert.Equal(t, before.Modes[i].Value, m.Value, "mode %d (ANSI %v) changed", m.Mode, m.ANSI)
			}
		}
		assert.Equal(t, before.Title, during.Title)
		assert.Equal(t, before.Pwd, during.Pwd)
		assert.Equal(t, before.Cursor, during.Cursor)
	})
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowAnswersQueriesWhilePromptingOnce(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.prompt(func() {
		h.app("\x1b[6n")
		h.waitContainer("\x1b[1;1R")
	})
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "answered once")
	assert.True(t, h.snapshotted(), "a replay would make the real terminal answer again")
}

func TestShowKeepsTheSavedCursor(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("ab\x1b7cd")
	h.prompt(nil)
	h.app("\x1b8Z")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowKeepsTheKittyStack(t *testing.T) {
	for name, before := range map[string]string{
		"primary screen":   "\x1b[>1u\x1b[>3u",
		"alternate screen": "\x1b[?1049h\x1b[>1u\x1b[>3u",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 20, 6)
			h.app(before)
			h.prompt(nil)
			for range 2 {
				h.app("\x1b[<u")
				want, err := h.term.shadow.KittyKeyboardFlags()
				require.NoError(t, err)
				got, err := h.real.KittyKeyboardFlags()
				require.NoError(t, err)
				assert.Equal(t, want, got)
			}
		})
	}
}

func TestShowKeysGoToThePrompt(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.keys("before")
	h.waitContainer("before")
	h.prompt(nil)
	h.keys("after")
	h.waitContainer("beforeafter")
}

func TestShowBarrierTimeout(t *testing.T) {
	h := newHarness(t, 20, 6, silentTerminal(), withTiming(func(tm *timing) { tm.barrierWait = 50 * time.Millisecond }))
	h.app("$ ")
	for range barrierTimeoutsBeforeDegraded {
		assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrBarrierTimeout)
	}
	assert.NotContains(t, h.stdout.String(), "\x1b[?1047h", "nothing was drawn")
	h.locked(func(tm *Terminal) { assert.True(t, tm.barrierUnsupported) })
	h.prompt(nil)
	assert.Equal(t, barrierTimeoutsBeforeDegraded, strings.Count(h.stdout.String(), barrierQuery),
		"no barrier query once degraded")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowRestoresWhenTheProgramPanics(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), keyModel{text: "PROMPT", panicOnKey: true})
	h.waitScreen("PROMPT")
	h.keys("x")
	assert.ErrorIs(t, h.result(done), tea.ErrProgramPanic)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
	h.keys("k")
	h.waitContainer("k")
}

func TestShowCancelled(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.show(ctx, promptModel)
	h.waitScreen("PROMPT")
	cancel()
	assert.ErrorIs(t, h.result(done), context.Canceled)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowEndsWhenTheContainerExits(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	h.out.close()
	assert.ErrorIs(t, h.result(done), ErrClosed)
	<-h.done
	assert.NoError(t, h.err)
	assert.NotContains(t, screenText(h.real), "PROMPT")
}

func TestShowEndsAtStdinEOF(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	done := h.show(context.Background(), promptModel)
	h.waitScreen("PROMPT")
	require.NoError(t, h.stdin.Close())
	assert.ErrorIs(t, h.result(done), ErrClosed)
	assert.NotContains(t, screenText(h.real), "PROMPT")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowAfterRunReturned(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.out.close()
	<-h.done
	written := h.stdout.String()
	assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrClosed)
	assert.Equal(t, written, h.stdout.String(), "nothing written")
}

func TestShowLeaveIsAtomic(t *testing.T) {
	h := newHarness(t, 20, 6)
	stop, flooded := make(chan struct{}), make(chan struct{})
	h.prompt(func() {
		go func() {
			defer close(flooded)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if !h.out.push(fmt.Sprintf("\x1b[3%dm%d\x1b[0m\r\n", i%8, i)) {
					return
				}
			}
		}()
	})
	close(stop)
	<-flooded
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowWaitsForAnEarlierShow(t *testing.T) {
	h := newHarness(t, 20, 6)
	first := h.show(context.Background(), keyModel{text: "FIRST"})
	h.waitScreen("FIRST")
	second := h.show(context.Background(), keyModel{text: "SECOND"})
	time.Sleep(50 * time.Millisecond)
	assert.NotContains(t, screenText(h.real), "SECOND")
	h.keys("x")
	require.NoError(t, h.result(first))
	h.waitScreen("SECOND")
	h.keys("x")
	require.NoError(t, h.result(second))
}

func TestShowWithoutShadowIsUnavailable(t *testing.T) {
	h := newHarness(t, 20, 6, withoutShadow())
	assert.ErrorIs(t, h.term.Show(context.Background(), promptModel), ErrUnavailable)
}

func TestResizeWhilePrompting(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.app("$ ")
	h.prompt(func() {
		h.waitScreen("PROMPT W=20")
		h.resize(30, 8)
		h.waitScreen("PROMPT W=30")
		h.app("output for the new width")
	})
	assert.True(t, h.snapshotted(), "the log was written for the old size")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShowFloodPastLogCap(t *testing.T) {
	h := newHarness(t, 20, 6, withTiming(func(tm *timing) { tm.logMax = 1024 }))
	h.prompt(func() {
		start := time.Now()
		for i := range 200 {
			h.app(fmt.Sprintf("y %d\r\n", i))
		}
		assert.Less(t, time.Since(start), 5*time.Second, "output never waits for the prompt")
		h.locked(func(tm *Terminal) { assert.True(t, tm.log.overflow) })
	})
	assert.True(t, h.snapshotted())
	assertSameTerminal(t, h.term.shadow, h.real)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./overlay/ -run 'TestShow|TestResizeWhilePrompting'`
Expected: FAIL to compile: `h.term.Show undefined`.

- [ ] **Step 3: Write `Show`**

`overlay/show.go`:

```go
package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Show runs model over the session and restores the screen afterwards. It
// waits for an earlier Show to finish. It returns ErrUnavailable when the
// session has no working shadow, ErrBarrierTimeout when the terminal
// didn't confirm the input switch (nothing was drawn), and ErrClosed when
// the session ends first. After the detach every path restores the screen,
// also when the program fails.
func (t *Terminal) Show(ctx context.Context, model tea.Model) error {
	t.showMu.Lock()
	defer t.showMu.Unlock()
	if err := t.detachAt(ctx); err != nil {
		return err
	}
	in, err := t.awaitBarrier(ctx)
	if err != nil {
		t.leave(false)
		return err
	}
	return t.runPrompt(ctx, in, model)
}

// awaitBarrier is T2. After barrierTimeoutsBeforeDegraded timeouts in a
// row, the session waits for input silence instead.
func (t *Terminal) awaitBarrier(ctx context.Context) (io.Reader, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-t.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	t.mu.Lock()
	degraded := t.barrierUnsupported
	t.mu.Unlock()
	var in io.Reader
	var err error
	if degraded {
		in, err = t.in.AwaitSilence(ctx, t.timing.silence)
	} else {
		in, err = t.in.AwaitBarrier(ctx, t.timing.barrierWait)
	}
	if err != nil && t.isDone() {
		err = ErrClosed
	}
	if degraded {
		return in, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case err == nil:
		t.barrierTimeouts = 0
	case errors.Is(err, ErrBarrierTimeout):
		t.barrierTimeouts++
		if t.barrierTimeouts == barrierTimeoutsBeforeDegraded {
			t.barrierUnsupported = true
			t.logf("overlay: the terminal doesn't answer DSR 5n; prompts now take input after %v without typing, so a reply or key in flight may reach the wrong side", t.timing.silence)
		}
	}
	return in, err
}

// runPrompt writes the enter sequence, runs the program and always ends
// with T3.
func (t *Terminal) runPrompt(ctx context.Context, in io.Reader, model tea.Model) (err error) {
	t.mu.Lock()
	_, _ = io.WriteString(t.cfg.Stdout, enterSeq(enterFor(t.cut)))
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	defer t.leave(true)

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// The session ending cancels the program.
		select {
		case <-t.done:
		case <-t.in.Done():
		case <-pctx.Done():
		}
		cancel()
	}()
	p := tea.NewProgram(model,
		tea.WithContext(pctx),
		tea.WithInput(in),
		tea.WithOutput(NewFilter(t.cfg.Stdout)),
		tea.WithWindowSize(cols, rows),
		tea.WithColorProfile(t.profile),
		tea.WithEnvironment(t.environ),
		tea.WithoutSignalHandler(),
	)
	t.setProgram(p)
	defer t.setProgram(nil)
	defer func() {
		// Bubble Tea recovers panics in the model; this covers its own
		// code, so the leave still runs.
		if r := recover(); r != nil {
			err = fmt.Errorf("overlay: prompt panicked: %v", r)
		}
	}()
	_, err = p.Run()
	switch {
	case t.isDone(), isClosed(t.in.Done()):
		return ErrClosed
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return err
}

func (t *Terminal) setProgram(p *tea.Program) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prog = p
}
```

- [ ] **Step 4: Run the tests, also under the race detector**

Run: `go test ./overlay/ -v -run 'TestShow|TestResizeWhilePrompting' && CGO_ENABLED=1 go test -race ./overlay/`
Expected: PASS. These tests drive Bubble Tea for real; under `-race` a run takes several seconds. If one fails, look at `h.stdout.String()` first: it is the exact byte stream the real terminal got, so a missing or misordered restore sequence shows up there. A prompt that never draws usually means the filter dropped something Bubble Tea's renderer needs (see Finding 1) or the barrier reply didn't reach `InputMux`.

- [ ] **Step 5: Commit**

```bash
gofmt -l overlay
git add overlay/show.go overlay/show_test.go
git commit -m "Add overlay.Terminal.Show: prompts over the session

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Attach sessions through `overlay.Terminal`

**Files:**
- Modify: `container/terminal.go`, `container/client.go`
- Test: `container/terminal_test.go`

**Interfaces:**
- Consumes: `overlay.New`, `overlay.Config`, `(*overlay.Terminal).Run`, `Resize`, `Show`, `overlay.ErrUnavailable` (Tasks 8–10).
- Produces: `type container.AttachOption func(*attachOptions)`; `func container.WithTerminal(fn func(*overlay.Terminal)) AttachOption`; `func container.WithLogf(fn func(format string, args ...any)) AttachOption`; `func (*Client) AttachAndStartSession(ctx, containerID string, opts ...AttachOption) error`; `func (*Client) ExecSession(ctx, containerID string, opts ...AttachOption) error`. Without `WithTerminal` the session runs with `overlay.Config.NoShadow`.

- [ ] **Step 1: Write the failing tests**

Append to `container/terminal_test.go`, and extend its imports to `bytes`, `context`, `io`, `net`, `os`, `sync`, `sync/atomic`, `testing`, `time`, `github.com/bernd/vibepit/overlay`, `github.com/docker/docker/api/types`, testify `assert` and `require`:

```go
func TestBuildAttachOptions(t *testing.T) {
	var got []string
	o := buildAttachOptions([]AttachOption{
		WithTerminal(func(*overlay.Terminal) { got = append(got, "terminal") }),
		WithLogf(func(string, ...any) { got = append(got, "log") }),
	})
	o.onTerminal(nil)
	o.logf("x")
	assert.Equal(t, []string{"terminal", "log"}, got)
	assert.Nil(t, buildAttachOptions(nil).onTerminal)
}

// lockedBuffer is a bytes.Buffer the session goroutines can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNewSessionTerminal(t *testing.T) {
	client, server := net.Pipe()
	resp := types.NewHijackedResponse(client, "")
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	stdout := &lockedBuffer{}
	var resized [][2]uint
	st := newSessionTerminal(resp, stdinR, stdout,
		func() (int, int, error) { return 80, 24, nil },
		func(height, width uint) { resized = append(resized, [2]uint{height, width}) },
		buildAttachOptions(nil))
	runDone := make(chan error, 1)
	go func() { runDone <- st.Run(context.Background()) }()

	_, err := server.Write([]byte("hello"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return stdout.String() == "hello" }, 5*time.Second, time.Millisecond)

	go func() { _, _ = stdinW.Write([]byte("ls\r")) }()
	buf := make([]byte, 3)
	_, err = io.ReadFull(server, buf)
	require.NoError(t, err)
	assert.Equal(t, "ls\r", string(buf))

	st.Resize()
	assert.Equal(t, [][2]uint{{24, 80}}, resized, "Docker takes the height first")

	assert.ErrorIs(t, st.Show(context.Background(), nil), overlay.ErrUnavailable, "no shadow without WithTerminal")

	require.NoError(t, server.Close())
	select {
	case err := <-runDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't end with the container output")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./container/ -run 'TestBuildAttachOptions|TestNewSessionTerminal'`
Expected: FAIL to compile: `undefined: buildAttachOptions`, `undefined: WithTerminal`, `undefined: newSessionTerminal`.

- [ ] **Step 3: Route `runTTYSession` through the overlay**

In `container/terminal.go`, add this paragraph to the package doc right after "…(cli/command/container/hijack.go)." :

```go
// It forwards through an overlay.Terminal, which can show prompts over
// the session (see WithTerminal).
```

Replace the imports with:

```go
import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/docker/docker/api/types"
	"golang.org/x/term"
)
```

Add after `ExitError.Error`:

```go
// AttachOption configures an interactive session started by
// AttachAndStartSession or ExecSession.
type AttachOption func(*attachOptions)

type attachOptions struct {
	onTerminal func(*overlay.Terminal)
	logf       func(format string, args ...any)
}

// WithTerminal passes the session's terminal to fn before any data flows,
// so the caller can show prompts over the session. Without it the session
// runs without a shadow terminal and can't prompt.
func WithTerminal(fn func(*overlay.Terminal)) AttachOption {
	return func(o *attachOptions) { o.onTerminal = fn }
}

// WithLogf receives the session terminal's diagnostics. The session owns
// the screen, so they can't go to stderr.
func WithLogf(fn func(format string, args ...any)) AttachOption {
	return func(o *attachOptions) { o.logf = fn }
}

func buildAttachOptions(opts []AttachOption) attachOptions {
	var o attachOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// newSessionTerminal connects a hijacked session to the local terminal.
func newSessionTerminal(resp types.HijackedResponse, stdin io.Reader, stdout io.Writer, size func() (int, int, error), resizeFn func(height, width uint), o attachOptions) *overlay.Terminal {
	return overlay.New(overlay.Config{
		Stdin:        stdin,
		Stdout:       stdout,
		ContainerIn:  resp.Conn,
		ContainerOut: resp.Reader,
		CloseInput:   resp.CloseWrite,
		Resize:       func(cols, rows int) { resizeFn(uint(rows), uint(cols)) },
		Size:         size,
		Logf:         o.logf,
		NoShadow:     o.onTerminal == nil,
	})
}
```

Replace `runTTYSession` with:

```go
// runTTYSession puts the host terminal into raw mode, forwards stdio to and
// from the hijacked Docker connection through an overlay.Terminal, and
// handles SIGWINCH for terminal resizing. The resizeFn is called with
// (height, width) whenever the terminal changes size. The function blocks
// until the container-side stream ends, then returns any error.
func runTTYSession(ctx context.Context, resp types.HijackedResponse, resizeFn func(height, width uint), opts attachOptions) error {
	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)

	size := func() (int, int, error) { return term.GetSize(fd) }
	t := newSessionTerminal(resp, os.Stdin, os.Stdout, size, resizeFn, opts)
	if opts.onTerminal != nil {
		opts.onTerminal(t)
	}

	// Set initial terminal size with retry. The container/exec process may
	// not be ready to accept a resize immediately after attach.
	go func() {
		for attempt := range 5 {
			if _, _, err := size(); err == nil {
				t.Resize()
				return
			}
			time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
		}
	}()

	// Forward SIGWINCH to the container.
	sigCh := make(chan os.Signal, 1)
	notifyResize(sigCh)
	defer signal.Stop(sigCh)
	// signal.Stop unregisters delivery but does not close sigCh.
	// done gives the resize watcher an explicit shutdown path.
	done := make(chan struct{})
	defer close(done)
	go watchResizeSignals(sigCh, done, t.Resize)

	// Run returns when the output ends, so the deferred restore runs as
	// soon as the container is done, as before.
	return t.Run(ctx)
}
```

`sync` is no longer imported: the `sync.OnceFunc` restore is gone because `Run` returns as soon as the output ends.

- [ ] **Step 4: Thread the options through the client**

In `container/client.go`:

```go
func (c *Client) AttachAndStartSession(ctx context.Context, containerID string, opts ...AttachOption) error {
```

and in its body

```go
	if err := runTTYSession(ctx, resp, resizeFn, buildAttachOptions(opts)); err != nil {
```

Likewise:

```go
func (c *Client) ExecSession(ctx context.Context, containerID string, opts ...AttachOption) error {
```

```go
	if err := runTTYSession(ctx, hijack, resizeFn, buildAttachOptions(opts)); err != nil {
```

- [ ] **Step 5: Run the tests**

Run: `go test ./container/ ./cmd/ && CGO_ENABLED=1 go test -race ./container/`
Expected: PASS. `cmd` still compiles because the new parameters are variadic.

- [ ] **Step 6: Commit**

```bash
gofmt -l container
git add container/terminal.go container/terminal_test.go container/client.go
git commit -m "Attach container sessions through overlay.Terminal

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: `run --prompt`: poller, prompter and prompt log

**Files:**
- Create: `cmd/prompt.go`, `cmd/prompt_test.go`, `cmd/promptlog.go`, `cmd/promptlog_test.go`
- Modify: `cmd/run.go`, `cmd/approve_ui_test.go`

**Interfaces:**
- Consumes: `approveScreen` (Task 4); `ControlClient.Logs`, `LogsAfter`, `Check` (Task 3); `container.WithTerminal`, `WithLogf`, the variadic attach methods (Task 11); `(*overlay.Terminal).Show`, `overlay.ErrBarrierTimeout`, `overlay.ErrClosed`, `overlay.ErrUnavailable`, `overlay.NewFilter`; `tui.NewWindow`, `tui.HeaderInfo`, `tui.Status`, `tui.SanitizeText`; `pollInterval`; `sessionBaseDir`; `(*ctr.Client).FindProxyContainerID`, `FindControlPort`.
- Produces: `const promptFlag = "prompt"`; `var promptCLIFlag *cli.BoolFlag`; `type promptFunc func(ctx context.Context, entry proxy.LogEntry) error`; `type blockWatcher` (`Next`, `Forget`); `func runBlockPrompter(ctx, client *ControlClient, interval time.Duration, prompt promptFunc)`; `func startPrompterLoop(ctx, cc *ControlClient, interval, grace time.Duration, prompt promptFunc) func()`; `type prompter`; `type blockPrompter` (`AttachOptions() []ctr.AttachOption`, `Stop()`); `func startBlockPrompter(ctx, cmd *cli.Command, getSession func() (*SessionInfo, error)) (*blockPrompter, error)`; `func loggedPrompt(logger *log.Logger, prompt promptFunc) promptFunc`; `func sessionInfoForRunning(ctx, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error)`; `var promptLogDir func() string`; `func openPromptLog(sessionID string) (*log.Logger, string)`.

- [ ] **Step 1: Write the failing tests for the prompt log**

`cmd/promptlog_test.go`:

```go
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptLog(t *testing.T) {
	dir := t.TempDir()
	old := promptLogDir
	promptLogDir = func() string { return dir }
	t.Cleanup(func() { promptLogDir = old })

	stale := filepath.Join(dir, "stale.log")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o600))
	past := time.Now().Add(-promptLogMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(stale, past, past))
	fresh := filepath.Join(dir, "fresh.log")
	require.NoError(t, os.WriteFile(fresh, []byte("x"), 0o600))

	logger, path := openPromptLog("sess1")
	assert.Equal(t, filepath.Join(dir, "sess1.log"), path)
	assert.NoFileExists(t, stale, "logs untouched for a week are removed")
	assert.FileExists(t, fresh)

	logger.Print("first")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "first")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), maxPromptLog-10), 0o600))
	logger.Print("does not fit")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, data, maxPromptLog-10, "past the cap, lines are dropped")
}
```

- [ ] **Step 2: Write the failing tests for the poller and the prompter setup**

`cmd/prompt_test.go` (`TestBlockWatcher_Next`, `TestRunBlockPrompter` and `TestRunBlockPrompter_RetriesPriming` are ported unchanged from `add/kitty-prompt`; its `TestStartBlockPrompter_Policy` is kitty-specific and is replaced):

```go
package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestBlockWatcher_Next(t *testing.T) {
	block := func(id uint64, src proxy.Source, domain, port string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: src, Domain: domain, Port: port, Action: proxy.ActionBlock}
	}
	allow := func(id uint64, domain string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: proxy.SourceProxy, Domain: domain, Port: "443", Action: proxy.ActionAllow}
	}

	tests := []struct {
		name    string
		batches [][]proxy.LogEntry
		want    [][]string // allow values per batch
		cursor  uint64
	}{
		{
			name:    "ignores allowed entries",
			batches: [][]proxy.LogEntry{{allow(1, "a.com"), block(2, proxy.SourceProxy, "b.com", "443")}},
			want:    [][]string{{"b.com:443"}},
			cursor:  2,
		},
		{
			name: "dedupes same target across batches",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceProxy, "b.com", "443"), block(2, proxy.SourceProxy, "b.com", "443")},
				{block(3, proxy.SourceProxy, "b.com", "443"), block(4, proxy.SourceProxy, "b.com", "80")},
			},
			want:   [][]string{{"b.com:443"}, {"b.com:80"}},
			cursor: 4,
		},
		{
			name: "dns and proxy for same domain are distinct",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceDNS, "b.com", ""), block(2, proxy.SourceProxy, "b.com", "443")},
			},
			want:   [][]string{{"b.com", "b.com:443"}},
			cursor: 2,
		},
		{
			name:    "empty batch keeps cursor",
			batches: [][]proxy.LogEntry{{block(5, proxy.SourceProxy, "b.com", "443")}, {}},
			want:    [][]string{{"b.com:443"}, nil},
			cursor:  5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bw blockWatcher
			for i, batch := range tt.batches {
				got := bw.Next(batch)
				var vals []string
				for _, e := range got {
					vals = append(vals, e.Target().String())
				}
				assert.Equal(t, tt.want[i], vals, "batch %d", i)
			}
			assert.Equal(t, tt.cursor, bw.cursor)
		})
	}
}

func TestBlockWatcher_Forget(t *testing.T) {
	var bw blockWatcher
	e := proxy.LogEntry{ID: 1, Source: proxy.SourceProxy, Domain: "b.com", Port: "443", Action: proxy.ActionBlock}
	require.Len(t, bw.Next([]proxy.LogEntry{e}), 1)
	e.ID = 2
	assert.Empty(t, bw.Next([]proxy.LogEntry{e}))
	bw.Forget(e.Target())
	e.ID = 3
	assert.Len(t, bw.Next([]proxy.LogEntry{e}), 1, "a forgotten target prompts again")
}

func TestRunBlockPrompter(t *testing.T) {
	tp := newTestProxy(t)
	log, client := tp.log, tp.client
	// Blocked before the poller starts: must not prompt.
	log.Add(proxy.LogEntry{Domain: "old.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	prompted := make(chan proxy.LogEntry, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(ctx, client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
			prompted <- e
			return nil
		})
	}()

	// Give the poller a moment to prime its cursor past old.com.
	time.Sleep(20 * time.Millisecond)
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	log.Add(proxy.LogEntry{Domain: "fine.com", Port: "443", Action: proxy.ActionAllow, Source: proxy.SourceProxy})

	select {
	case e := <-prompted:
		assert.Equal(t, "new.com", e.Domain)
	case <-time.After(time.Second):
		t.Fatal("no prompt for new.com")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop on cancel")
	}
	select {
	case e := <-prompted:
		t.Fatalf("unexpected extra prompt for %s", e.Domain)
	default:
	}
}

func TestRunBlockPrompter_RetriesPriming(t *testing.T) {
	tp := newTestProxy(t)
	log, api := tp.log, tp.api
	log.Add(proxy.LogEntry{Domain: "old.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	// Fail the first requests so the priming call does not succeed at once.
	var calls atomic.Int32
	client := testControlClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		api.ServeHTTP(w, r)
	}))

	prompted := make(chan proxy.LogEntry, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runBlockPrompter(ctx, client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		return nil
	})

	require.Eventually(t, func() bool { return calls.Load() > 5 }, time.Second, 5*time.Millisecond)
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	select {
	case e := <-prompted:
		assert.Equal(t, "new.com", e.Domain, "history from before the poller started must not be replayed")
	case <-time.After(time.Second):
		t.Fatal("no prompt for new.com")
	}
}

func TestRunBlockPrompter_ReasksAfterBarrierTimeout(t *testing.T) {
	tp := newTestProxy(t)
	prompted := make(chan proxy.LogEntry, 10)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runBlockPrompter(ctx, tp.client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		if calls.Add(1) == 1 {
			return fmt.Errorf("show: %w", overlay.ErrBarrierTimeout)
		}
		return nil
	})
	time.Sleep(20 * time.Millisecond)
	block := proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy}
	for i := range 2 {
		tp.log.Add(block)
		select {
		case <-prompted:
		case <-time.After(time.Second):
			t.Fatalf("no prompt %d", i+1)
		}
	}
	tp.log.Add(block)
	select {
	case <-prompted:
		t.Fatal("a shown prompt is not repeated")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunBlockPrompter_BurstPastTail(t *testing.T) {
	tp := newTestProxy(t)
	prompted := make(chan proxy.LogEntry, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runBlockPrompter(ctx, tp.client, 20*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		return nil
	})
	time.Sleep(50 * time.Millisecond) // primed on an empty log
	n := proxy.TailSize + 5
	for i := range n {
		tp.log.Add(proxy.LogEntry{Domain: fmt.Sprintf("d%d.com", i), Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	}
	seen := map[string]bool{}
	for len(seen) < n {
		select {
		case e := <-prompted:
			seen[e.Domain] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d targets prompted", len(seen), n)
		}
	}
}

func TestStartBlockPrompter(t *testing.T) {
	var lookups atomic.Int32
	counting := func() (*SessionInfo, error) {
		lookups.Add(1)
		return nil, errors.New("find control port: not published")
	}
	missingCreds := func() (*SessionInfo, error) {
		return &SessionInfo{ControlPort: "1", SessionID: "no-such-session-for-prompt-test"}, nil
	}
	tests := []struct {
		name        string
		args        []string
		getSession  func() (*SessionInfo, error)
		wantErr     string
		wantLookups int32
	}{
		{name: "off by default", args: []string{"x"}, getSession: counting},
		{name: "explicitly off", args: []string{"x", "--prompt=false"}, getSession: counting},
		{name: "session lookup fails", args: []string{"x", "--prompt"}, getSession: counting, wantErr: "not published", wantLookups: 1},
		{name: "missing credentials", args: []string{"x", "--prompt"}, getSession: missingCreds, wantErr: "load TLS credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups.Store(0)
			var bp *blockPrompter
			var gotErr error
			cmd := &cli.Command{
				Name:  "x",
				Flags: []cli.Flag{promptCLIFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					bp, gotErr = startBlockPrompter(ctx, cmd, tt.getSession)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tt.args))
			require.NotNil(t, bp)
			assert.Empty(t, bp.AttachOptions(), "no prompter, no attach options")
			bp.Stop()
			assert.Equal(t, tt.wantLookups, lookups.Load())
			if tt.wantErr == "" {
				assert.NoError(t, gotErr)
			} else {
				assert.ErrorContains(t, gotErr, tt.wantErr)
			}
		})
	}
}

func TestLoggedPrompt(t *testing.T) {
	entry := proxy.LogEntry{Source: proxy.SourceProxy, Domain: "evil\x1b[2J.com", Port: "443", Action: proxy.ActionBlock}
	tests := []struct {
		name    string
		err     error
		wantLog string
	}{
		{name: "shown"},
		{name: "cancelled", err: context.Canceled},
		{name: "session ended", err: fmt.Errorf("show: %w", overlay.ErrClosed)},
		{name: "unavailable", err: fmt.Errorf("%w: trap", overlay.ErrUnavailable), wantLog: "prompt for evil[2J.com:443: overlay: unavailable: trap\n"},
		{name: "barrier timeout", err: overlay.ErrBarrierTimeout, wantLog: "prompt for evil[2J.com:443: overlay: terminal did not answer the barrier query\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			prompt := loggedPrompt(log.New(&buf, "", 0), func(context.Context, proxy.LogEntry) error { return tt.err })
			assert.Equal(t, tt.err, prompt(context.Background(), entry))
			assert.Equal(t, tt.wantLog, buf.String())
		})
	}
}
```

- [ ] **Step 3: Pin the approve screen through the prompt filter**

Append to `cmd/approve_ui_test.go`, and add `bytes`, `io`, `time`, `github.com/bernd/vibepit/overlay`, `github.com/bernd/vibepit/vt` and `github.com/charmbracelet/colorprofile` to its imports:

```go
// The prompt's output passes overlay.NewFilter, which drops every query.
// The approve screen must look the same through it as on a cooked TTY,
// whatever TERM makes Bubble Tea's renderer use, and still take keys.
func TestApproveScreen_RendersThroughPromptFilter(t *testing.T) {
	for _, term := range []string{"xterm-256color", "xterm-ghostty", "screen", "linux"} {
		t.Run(term, func(t *testing.T) {
			tp := newTestProxy(t)
			session := &SessionInfo{SessionID: "test123456", ProjectDir: t.TempDir()}
			header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
			start := func(out io.Writer, in io.Reader) (*tea.Program, <-chan error) {
				p := tea.NewProgram(tui.NewWindow(header, newApproveScreen(session, tp.client, blockedEntry)),
					tea.WithInput(in),
					tea.WithOutput(out),
					tea.WithWindowSize(100, 30),
					tea.WithColorProfile(colorprofile.TrueColor),
					tea.WithEnvironment([]string{"TERM=" + term}),
					tea.WithoutSignalHandler(),
				)
				errc := make(chan error, 1)
				go func() {
					_, err := p.Run()
					errc <- err
				}()
				return p, errc
			}
			filtered, direct := newScreen(t, 100, 30), newScreen(t, 100, 30)
			keysR, keysW := io.Pipe()
			idleR, idleW := io.Pipe()
			t.Cleanup(func() {
				_ = keysW.Close()
				_ = idleW.Close()
			})
			_, filteredDone := start(overlay.NewFilter(filtered), keysR)
			directProg, directDone := start(onlcr{direct}, idleR)

			require.Eventually(t, func() bool {
				got := screenText(filtered)
				return strings.Contains(got, "Allow this connection?") && got == screenText(direct)
			}, 10*time.Second, 20*time.Millisecond)

			_, err := keysW.Write([]byte("a"))
			require.NoError(t, err)
			select {
			case err := <-filteredDone:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the prompt didn't take the key")
			}
			assert.True(t, tp.http.Allows("api.example.com", "443"))
			directProg.Kill()
			<-directDone
		})
	}
}

// onlcr maps LF to CR LF, as a TTY with ONLCR does. Bubble Tea's renderer
// counts on it when its input isn't a TTY.
type onlcr struct{ w io.Writer }

func (o onlcr) Write(p []byte) (int, error) {
	if _, err := o.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func newScreen(t *testing.T, cols, rows int) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(uint16(cols), uint16(rows))
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

// screenText is the visible screen as plain text, or "" on error, so it is
// safe inside require.Eventually.
func screenText(term *vt.Terminal) string {
	b, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true, Region: vt.RegionScreen})
	if err != nil {
		return ""
	}
	return string(b)
}
```

- [ ] **Step 4: Run them to see them fail**

Run: `go test ./cmd/ -run 'TestPromptLog|TestBlockWatcher|TestRunBlockPrompter|TestStartBlockPrompter|TestLoggedPrompt|TestApproveScreen_RendersThroughPromptFilter'`
Expected: FAIL to compile: `undefined: promptLogDir`, `undefined: blockWatcher`, `undefined: runBlockPrompter`, `undefined: promptCLIFlag`, `undefined: loggedPrompt`.

- [ ] **Step 5: Write the prompt log**

`cmd/promptlog.go`:

```go
package cmd

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Prompt logs hold what prompts couldn't show on the terminal, one file per
// session: the session owns the terminal, so there's nowhere else to put
// it. They live beside the session directories, not in one, because those
// are removed when the session stops, which is when a log gets read.
const (
	promptLogDirName = "prompt-logs"
	maxPromptLog     = 1 << 20 // past it, lines are dropped
	promptLogMaxAge  = 7 * 24 * time.Hour
)

// promptLogDir is where prompt logs go; tests point it elsewhere.
var promptLogDir = func() string {
	return filepath.Join(filepath.Dir(sessionBaseDir()), promptLogDirName)
}

// openPromptLog returns a logger that appends to the session's prompt log,
// and the log's path, after removing logs untouched for promptLogMaxAge.
// Logging is best effort: without the file, lines are dropped.
func openPromptLog(sessionID string) (*log.Logger, string) {
	dir := promptLogDir()
	path := filepath.Join(dir, sessionID+".log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return log.New(io.Discard, "", 0), path
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > promptLogMaxAge {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return log.New(&promptLogWriter{path: path}, "", log.LstdFlags), path
}

// promptLogWriter opens the file for every line. Lines are rare, the cap
// then holds across clients sharing the file, and a log removed as old
// comes back.
type promptLogWriter struct {
	mu   sync.Mutex
	path string
}

func (w *promptLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return len(p), nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size()+int64(len(p)) > maxPromptLog {
		return len(p), nil
	}
	_, _ = f.Write(p)
	return len(p), nil
}
```

- [ ] **Step 6: Write the poller and the prompter**

`cmd/prompt.go`:

```go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const promptFlag = "prompt"

var promptCLIFlag = &cli.BoolFlag{
	Name:  promptFlag,
	Usage: "Show an allow/deny prompt over the session when the proxy blocks a connection",
}

// promptFunc asks the user about a blocked entry. The prompt posts the
// decision to the control API itself.
type promptFunc func(ctx context.Context, entry proxy.LogEntry) error

// blockWatcher tracks the log cursor and which blocked targets have already
// been surfaced, so each domain:port triggers at most one prompt per session
// even though the agent typically retries a blocked request many times.
type blockWatcher struct {
	cursor uint64
	seen   map[proxy.Target]bool
}

// Next returns the blocked entries in batch that have not been seen before
// and advances the cursor past the batch.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) []proxy.LogEntry {
	if bw.seen == nil {
		bw.seen = make(map[proxy.Target]bool)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		bw.cursor = e.ID
		if e.Action != proxy.ActionBlock {
			continue
		}
		key := e.Target()
		if bw.seen[key] {
			continue
		}
		bw.seen[key] = true
		fresh = append(fresh, e)
	}
	return fresh
}

// Forget lets target prompt again on its next block.
func (bw *blockWatcher) Forget(target proxy.Target) {
	delete(bw.seen, target)
}

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Poll
// errors are retried on the next tick; prompt failures are for the prompt
// function to report, see loggedPrompt.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt promptFunc) {
	var bw blockWatcher
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Skip whatever was blocked before we started watching: start the
	// cursor at the newest entry. Retry until that succeeds, otherwise the
	// first poll would replay old blocks.
	for {
		entries, err := client.Logs()
		if err == nil {
			if len(entries) > 0 {
				bw.cursor = entries[len(entries)-1].ID
			}
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := client.LogsAfter(bw.cursor)
		if err != nil {
			continue
		}
		for _, e := range bw.Next(entries) {
			// Another client may have decided already. A failed check, e.g. an
			// older proxy without /check, falls back to prompting.
			if res, err := client.Check(e); err == nil && res.Decided() {
				continue
			}
			if err := prompt(ctx, e); errors.Is(err, overlay.ErrBarrierTimeout) {
				// Nothing was shown: ask again on the next block.
				bw.Forget(e.Target())
			}
		}
	}
}

// prompterStopGrace bounds how long shutdown waits for an open prompt to
// restore the screen.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background. The returned
// stop function cancels it and waits, up to grace, for it to return, so an
// open prompt is closed before the CLI exits.
func startPrompterLoop(ctx context.Context, cc *ControlClient, interval, grace time.Duration, prompt promptFunc) func() {
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(pctx, cc, interval, prompt)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(grace):
		}
	}
}

// prompter shows the approve screen over the session.
type prompter struct {
	term    *overlay.Terminal
	session *SessionInfo
	client  *ControlClient
}

func (p *prompter) Show(ctx context.Context, entry proxy.LogEntry) error {
	header := &tui.HeaderInfo{ProjectDir: p.session.ProjectDir, SessionID: p.session.SessionID}
	return p.term.Show(ctx, tui.NewWindow(header, newApproveScreen(p.session, p.client, entry)))
}

// blockPrompter is the prompting set up for one attach: the attach options
// that hand it the session's terminal, and a stop function.
type blockPrompter struct {
	opts []ctr.AttachOption
	stop func()
}

var noBlockPrompter = &blockPrompter{stop: func() {}}

// AttachOptions returns the options to pass to the session attach.
func (bp *blockPrompter) AttachOptions() []ctr.AttachOption { return bp.opts }

// Stop ends polling and closes an open prompt. Always safe to call.
func (bp *blockPrompter) Stop() { bp.stop() }

// startBlockPrompter sets up prompting when --prompt is set. The session
// and the control client are resolved before the attach takes over the
// terminal, so a failure prints normally and stops the command. Polling
// starts once the attach hands over the session's terminal.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	if !cmd.Bool(promptFlag) {
		return noBlockPrompter, nil
	}
	session, err := getSession()
	if err != nil {
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	}
	logger, logPath := openPromptLog(session.SessionID)

	var (
		mu       sync.Mutex
		stopped  bool
		stopLoop func()
	)
	onTerminal := func(t *overlay.Terminal) {
		mu.Lock()
		defer mu.Unlock()
		if stopped || stopLoop != nil {
			return
		}
		p := &prompter{term: t, session: session, client: cc}
		stopLoop = startPrompterLoop(ctx, cc, pollInterval, prompterStopGrace, loggedPrompt(logger, p.Show))
	}
	tui.Status("Prompting", "for blocked connections (log: %s)", logPath)
	return &blockPrompter{
		opts: []ctr.AttachOption{ctr.WithTerminal(onTerminal), ctr.WithLogf(logger.Printf)},
		stop: func() {
			mu.Lock()
			stopped = true
			s := stopLoop
			mu.Unlock()
			if s != nil {
				s()
			}
			cc.Close()
		},
	}, nil
}

// loggedPrompt logs why a prompt failed or wasn't shown: the session owns
// the terminal, so the prompt log is the only place for it. A prompt ended
// by the session ending isn't a failure.
func loggedPrompt(logger *log.Logger, prompt promptFunc) promptFunc {
	return func(ctx context.Context, e proxy.LogEntry) error {
		err := prompt(ctx, e)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			logger.Printf("prompt for %s: %v", tui.SanitizeText(e.Target().String()), err)
		}
		return err
	}
}

// sessionInfoForRunning builds the SessionInfo for an already running
// session from the proxy container's published control port.
func sessionInfoForRunning(ctx context.Context, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error) {
	proxyID, err := client.FindProxyContainerID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find proxy container: %w", err)
	}
	port, err := client.FindControlPort(ctx, proxyID)
	if err != nil {
		return nil, fmt.Errorf("find control port: %w", err)
	}
	return &SessionInfo{ControlPort: strconv.Itoa(port), SessionID: sessionID, ProjectDir: projectDir}, nil
}
```

- [ ] **Step 7: Wire `--prompt` into `run`**

`cmd/run.go`: add `"strconv"` to the imports, and:

```go
func RunCommand() *cli.Command {
	return &cli.Command{
		Name:   "run",
		Usage:  "Start the sandbox",
		Flags:  append(sandboxFlags(), promptCLIFlag),
		Action: RunAction,
	}
}
```

Replace the existing-session branch:

```go
	if existing != nil {
		prompter, err := startBlockPrompter(ctx, cmd, func() (*SessionInfo, error) {
			return sessionInfoForRunning(ctx, client, existing.SessionID, projectRoot)
		})
		if err != nil {
			return err
		}
		defer prompter.Stop()
		tui.Status("Attaching", "to running session in %s", projectRoot)
		return client.ExecSession(ctx, existing.ContainerID, prompter.AttachOptions()...)
	}
```

and the end of `RunAction`, after the deferred `StopAndRemove`:

```go
	prompter, err := startBlockPrompter(ctx, cmd, func() (*SessionInfo, error) {
		return &SessionInfo{
			ControlPort: strconv.Itoa(infra.Merged.ControlAPIPort),
			SessionID:   infra.SessionID,
			ProjectDir:  projectRoot,
		}, nil
	})
	if err != nil {
		return err
	}
	defer prompter.Stop()

	tui.Status("Starting", "sandbox container")
	tui.Status("Attaching", "shell session")
	fmt.Println()
	return client.AttachAndStartSession(ctx, sandboxContainer, prompter.AttachOptions()...)
```

`sandboxFlags()` returns a fresh slice on every call, so the `append` is safe. `prompter.Stop()` is deferred after the sandbox cleanup, so it runs first: the prompt closes before the container stops.

- [ ] **Step 8: Run the tests, also under the race detector**

Run: `go test ./cmd/ && CGO_ENABLED=1 go test -race ./cmd/`
Expected: PASS. If `TestApproveScreen_RendersThroughPromptFilter` never converges for one `TERM`, compare `screenText(filtered)` and `screenText(direct)`: the difference names the sequence the filter drops. Extend the allowlist only with sequences that move the cursor or edit content, and record the addition in the filter's comment and in the spec's Restoration Contract.

- [ ] **Step 9: Commit**

```bash
gofmt -l cmd
git add cmd/prompt.go cmd/prompt_test.go cmd/promptlog.go cmd/promptlog_test.go cmd/run.go cmd/approve_ui_test.go
git commit -m "Prompt for blocked connections over the session with run --prompt

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: Documentation, Makefile, full verification

**Files:**
- Modify: `docs/content/reference/cli.md`, `docs/content/explanations/threat-model.md`, `AGENTS.md`, `Makefile`

**Interfaces:**
- Consumes: everything above.
- Produces: user docs for `--prompt`; `make test-race` covers `./overlay` and `./container`.

- [ ] **Step 1: Document `--prompt` in the CLI reference**

In `docs/content/reference/cli.md`, `run` section, add a row to the Flags table after `-r`, `--reconfigure`:

```markdown
| `--prompt` | bool | `false` | Show an allow/deny prompt over the session when the proxy blocks a connection. See [Blocked connection prompt](#blocked-connection-prompt). |
```

Add to the end of the Behavior list:

```markdown
- With `--prompt`, blocked connections open an allow/deny prompt over the
  session. See [Blocked connection prompt](#blocked-connection-prompt).
```

Add this section between Behavior and Examples:

````markdown
### Blocked connection prompt

With `--prompt`, `run` asks you right away whether to allow a connection
the proxy blocked. The prompt appears over the session, in the same
terminal window, in any terminal emulator. Once you decide or dismiss it,
the agent's screen comes back as it was, including any output the agent
wrote in the meantime.

| Key | Action |
|-----|--------|
| `a` | Allow for the rest of the session |
| `A` | Allow and save to the project configuration |
| `n` | Deny. Other clients stop asking about this target for the rest of the session. |
| `Esc`, `q` | Dismiss without deciding. Other clients still ask. |

Behavior details:

- The blocked request has already failed when the prompt appears. Retry it
  after allowing.
- The agent keeps running while the prompt shows. Its output appears when
  the prompt closes.
- Each target prompts at most once per session, no matter how often the
  agent retries.
- Prompts for different targets open one after another, never on top of
  each other.
- When several clients are attached to one session, each shows the prompt.
  As soon as one of them allows or denies, the others close within about a
  second.
- Denied targets are held in memory by the proxy. They are forgotten when
  the session stops. To allow a denied target later, use
  [`allow-http`](#allow-http), [`allow-dns`](#allow-dns), or
  [`monitor`](#monitor).
- IPv6 address targets can be denied or dismissed, but not allowed. The
  allowlist does not support IPv6 literals.
- Before the prompt appears, vibepit asks the terminal for a status report
  to find the exact point where your typing ends and the prompt's input
  begins. If the terminal doesn't answer, the prompt is skipped and shown
  on the next block of that target. After two misses, vibepit instead
  switches input to the prompt after a short pause in typing.
- When a prompt can't be shown, the reason is written to the prompt log,
  `$XDG_STATE_HOME/vibepit/prompt-logs/<session>.log` (usually under
  `~/.local/state/`). `run --prompt` prints its path at startup.
- When the agent uses the whole screen (for example an editor), or the
  terminal is resized while the prompt shows, vibepit redraws the screen
  from its own copy. Lines that scrolled off in the meantime are then
  missing from the terminal's scrollback, and hyperlinks and images are not
  restored.
````

Add to the Examples block:

```bash
# Prompt to allow connections the proxy blocks
vibepit run --prompt
```

- [ ] **Step 2: Document the prompt in the threat model**

In `docs/content/explanations/threat-model.md`, add before `## Residual risks`:

```markdown
### Blocked connection prompts

With `vibepit run --prompt`, a blocked request from the sandbox opens an
allow/deny prompt over your session. See the
[CLI reference](../reference/cli.md#blocked-connection-prompt). This gives
the agent a way to put a question in front of you: it can make requests on
purpose to trigger prompts, and it chooses the domain names they show. A
domain can be made to look legitimate, for example
`api-anthropic.example.net`.

The decision stays with you. The prompt runs on the host, and it talks to
the proxy's control API with the host-only mTLS credentials, which never
enter the sandbox. While the prompt shows, the agent's output is held back
and your keys go to the prompt only after the terminal confirmed the
switch, so the agent can't draw over the prompt or read your answer. The
domain and reason shown come from the sandbox's own request, so control
characters and escape sequences are stripped before display. The agent can
print text that looks like a prompt, but the keys you type into it go to
the agent, and the proxy allows nothing without the real prompt.

Each target prompts at most once per session, and targets you deny stop
prompting on every client. An agent can still produce many prompts by
requesting many different domains. Read the domain in each prompt before
pressing `a`, and dismiss or deny anything you do not recognize.
```

- [ ] **Step 3: Update `AGENTS.md` and the Makefile**

In `AGENTS.md`, "Build, Run, and Test" code block, after `go run . -p vcs-github …`:

```bash
go run . --prompt            # prompt to allow blocked connections
```

In "Go CLI (`cmd/`)", extend the `run` bullet:

```markdown
- `run` creates an isolated network, starts proxy + sandbox containers, and
  manages persistent `vibepit-home` volume and per-session networking. With
  `--prompt` it polls the control API for blocked connections and shows the
  approve screen over the session through `overlay`.
```

Add a section after "Terminal emulator (`vt/`)":

```markdown
### Overlay (`overlay/`)

Shows a Bubble Tea program over a `run` session in any terminal. A shadow
`vt.Terminal` sees every container byte but never sits between the
container and the screen. `Terminal.Show` stops forwarding output at a byte
where the shadow's parser is at ground, hands stdin to the prompt at the
terminal's reply to a DSR 5n barrier query, and restores the screen by
replaying the output logged meanwhile or from the shadow. `InputMux` is the
only reader of stdin. `NewFilter` passes only the prompt output the restore
can undo. Used by `container.runTTYSession`; `cmd/prompt.go` shows the
approve screen through it. Only `overlay` imports `vt` on the host side.
```

In the `Makefile`, extend `test-race`:

```make
test-race:
	CGO_ENABLED=1 go test -race ./session ./sshd ./cmd ./vt/... ./overlay ./container
```

- [ ] **Step 4: Run the full verification**

```bash
gofmt -l cmd container overlay proxy tui ward integration_test.go
go vet ./...
make test
make test-integration
CGO_ENABLED=1 go test -race ./overlay/... ./container/... ./cmd/... ./proxy/...
git status --short
```

Expected: `gofmt -l` prints nothing; `go vet` passes; `make test` and `make test-integration` pass; the race run passes. `git status --short` shows nothing but untracked files under `docs/superpowers/`. `make test-race` also covers `./session`, which the Makefile documents as failing because of vt-go; that is expected and out of scope.

- [ ] **Step 5: Commit**

```bash
git add docs/content/reference/cli.md docs/content/explanations/threat-model.md AGENTS.md Makefile
git commit -m "Document run --prompt and the overlay package

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

## Manual follow-ups (not tasks; need the user)

- Step 3 of the rollout: the manual terminal matrix (kitty, ghostty, wezterm, tmux, xterm × shell prompt, vim, Claude Code, `yes`) with `go run . --prompt`, outside the nested sandbox. Record per cell: the prompt appears, keys reach it, `a` allows, the screen is intact afterwards, no stray bytes in the scrollback.
- Decide whether `--prompt` becomes the default after the matrix passes.
- Known consequence of the snapshot entering the alternate screen with `?1049h`: the real terminal saves its current cursor into the DECSC slot, which is the cut position, not where the app was when it entered the alternate screen while detached. It only matters when the app leaves that screen again and printed on the primary screen between the cut and its `?1049h`.
