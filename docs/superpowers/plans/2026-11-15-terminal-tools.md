# Desktop terminal tools implementation plan

> Implement inline; use the existing Svelte/Wails boundary and wterm 0.5.10 APIs.

**Goal:** Add Find, selection-to-Claude draft insertion, inactive-pane rendering pause,
shell status, and current-directory display to the desktop terminal.

**Design:** Extend the existing terminal with a compact toolbar and an optional
Find row. Search includes retained history, match count, case sensitivity, and
next/previous controls. Sending selection appends a fenced text block to the
existing Claude draft and focuses it; it never submits a prompt. Parse shell
reports as display-only metadata. Missing reports remain explicitly unavailable.
The default Bash configuration emits OSC 133 and OSC 7 for desktop-created
sessions, enabled through an SSH environment variable. Existing shells require
restarting after upgrading the image or explicitly sourcing the integration.

**Constraints:** Preserve theme, PTY byte transport, native edit actions, connection
isolation and startup cleanup. Do not infer shell state from output. Do not replace
user prompt hooks or DEBUG traps. Keep root vt/ unchanged. Existing untracked
experiment files belong to the user; do not commit the directory wholesale.

- [x] Add tested directory parsing, shell-status formatting and draft insertion helpers.
- [x] Add terminal toolbar/search and selection handoff; connect active-pane rendering.
- [x] Add Bash OSC hooks, opt-in desktop SSH environment and demo reports.
- [x] Verify frontend checks/tests/build, shell BATS and real interactive Bash,
      desktop Go tests, root tests as relevant, and built-browser user flows.
- [x] Review selection focus, stale async results, hidden-tab updates, hostile URI
      metadata, prompt-hook compatibility, and reconnect cleanup; document limitations.

Verification: frontend type check (zero warnings), 23 frontend tests, production
build, Svelte analyzer, and UI detector passed. Root unit/integration tests,
desktop server/purego tests and backend race checks passed. All 9 BATS tests
passed, including the interactive Bash PTY test. Built-browser checks passed for
search, selection draft handoff, paused rendering, OSC state/directory updates,
query limits, delayed hidden startup focus, and the existing input/menu/resize/
reconnect regressions. Independent review identified the wterm query-length
limit; the input and library boundary now enforce it. Native WebKit and live
container attachment remain host checks because those runtimes are unavailable.
