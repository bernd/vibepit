# Desktop Workspace Implementation Plan

> **For agentic workers:** Work the stages in order. Each stage ends with its own check. Do not report a host-only check as verified from inside the sandbox.

**Goal:** Port the workspace mockup (`experiments/desktop/vibepit-desktop.html`) to Svelte as a playground that runs against fake data, then host the real terminal in it.
**Architecture:** Framework-free TypeScript in `frontend/src/core/workspace/` holds the layout tree, commands, and the fake sandbox. Svelte 5 components in `frontend/src/workspace/` render it. A second Vite entry point, `workspace.html`, serves the playground beside the existing app.
**Tech Stack:** Svelte 5, TypeScript, Vite, `node --test`.
**Spec:** [Vibepit desktop workspace layout](../specs/2026-11-18-desktop-workspace-layout-design.md). Its acceptance checks are the checks for stages 2 to 5.

## Constraints and review focus

- `core/` imports nothing from Svelte or the DOM. Its tests run under `node --test` with type stripping, so it uses erasable TypeScript syntax only.
- Layout operations are pure. They return a new layout and leave their input untouched, so a preview never edits the committed layout.
- Preview and commit call the same operation with the same sizing rule.
- Do not change `App.svelte` or the existing entry point until stage 5. The current app must keep building and passing its tests.
- Keep the mockup as a frozen reference until the playground passes the spec's acceptance checks, then delete it.
- The fake sandbox stands in for vibed and the proxy. Components receive it through an interface that the real host clients can implement later.
- A hold arriving, a status change, and a redraw never move keyboard focus.
- Minimum pane sizes, hit-zone sizes, and hover timing stay tunable constants in one place.

## Stages

- [x] **1. Layout tree in `core/`.** Types, `transform` (dock and tab-bar moves), `split`, `close`, `normalize` (group merging), `equalize`, divider weights, minimum sizes, and directional neighbor lookup.
  Check: unit tests cover the spec's worked example, nested example, merging examples, size rule, focus after a move, rejection cases, and that no operation mutates its input.
- [x] **2. Workspace shell.** Playground entry point, tab bar, panes with placeholder content, group handles, dividers, zoom, pointer docking with in-place markers and a landing block, and the move dialog with option diagrams.
  Check: the docking, zoom, equalize, and persistence acceptance checks pass in the playground.
- [ ] **3. Held connections.** Fake network source, header readout, hold list, Monitor and Logs panes, expiry, late decisions, save failure, and connection states.
  Check: the hold acceptance checks pass, including focus return and rows that stay in place.
- [ ] **4. Commands.** Command registry, chords, command palette, **Send next key to terminal**, menus with focus return, and directional pane focus.
  Check: the keyboard acceptance checks pass. Every command in the spec's table is in the palette.
- [ ] **5. Sessions and the real terminal.** Fake session source with program state, close-pane rules, attach picker, session limit, rename, status badges, then `Terminal.svelte` inside a pane against the demo backend.
  Check: the session acceptance checks pass. `App.svelte` either hosts the workspace or is replaced by it.

## Status and handoff

As of 23 November 2026, stages 1 and 2 are done and committed on `experiment/desktop`. Stages 3 to 5 are not started. Read this section before starting one.

### What exists

| Path under `experiments/desktop/frontend` | Contents |
|---|---|
| `src/core/workspace/layout.ts` | Layout tree and its pure operations: `transform`, `split`, `close`, `normalize`, `equalize`, `setDivider`, `sameLayout`, `neighbor`. |
| `src/core/workspace/geometry.ts` | Pixel placement (`place`), divider limits, and docking hit-tests (`hitStage`, `edgeZone`, `crossRects`, `tabZone`). |
| `src/core/workspace/options.ts` | The choices a move has: `dockOptions` for the drag markers, `moveOptions` for the move dialog. |
| `src/core/workspace/persist.ts` | `restore`, which validates and repairs a saved layout. |
| `src/workspace/state.svelte.ts` | The `Workspace` class: layout, sessions, drafts, zoom, drag state, menus, and saving to localStorage. |
| `src/workspace/docking.ts` | Pointer docking: drag sources, the tab rest delay, markers, and commit or cancel. |
| `src/workspace/*.svelte` | `Workspace` (shell), `TabBar`, `Stage`, `PaneView`, `MoveDialog`, `Menu`, `Diagram`, `Icon`. |
| `workspace.html` | The playground entry point. `npm run dev`, then open `/workspace.html`. |
| `tests/workspace-*.test.mjs` | Unit tests for `core/`. |
| `tests/browser/workspace.test.mjs` | Playwright tests against the built playground. |

### Decisions made while building, beyond the spec text

- **Panes render as one flat keyed list.** `Stage.svelte` positions every pane of every tab from `place()` rectangles. A pane keeps its DOM when the tree changes or it moves to another tab. Anything a pane owns must survive being hidden, and view state lives in the `Workspace` class keyed by pane id, not in the component.
- **Pane content is a placeholder.** `PaneView.svelte` shows a draft field for sessions and one line of text for Monitor and Logs. Stages 3 and 5 replace these bodies.
- **Sessions are a stub.** `Workspace.sessions` holds only names. `closeNode` drops the name and ends nothing. Stage 5 replaces this with the fake session source.
- **The header has no readout yet**, and the footer has only **Restore example tabs**.
- **No chords exist yet.** Zoom, split, and close are reachable from buttons and menus only.
- **Docking changed after user testing.** The spec is up to date with all of it: edge zones with a sticky middle, the tab rest delay, no preview over the source, reorder keeps sizes, markers and the cross of arrows, the landing block, the group outline, and the option dialog.

### The mockup as a reference for stages 3 to 5

`experiments/desktop/vibepit-desktop.html` implements the behavior of stages 3 to 5 in plain JavaScript. Port the behavior, not the code. Its docking and its layout rules are out of date and must not be copied.

| Stage | Spec sections | Mockup functions to read |
|---|---|---|
| 3 | Held connections; Connection states | `holdRow`, `syncHolds`, `settleRow`, `decide`, `removeExpired`, `updateHeader`, `openHolds`, `closeHolds`, `network`, `paintNetwork`, `setConn`, `reconnect` |
| 4 | Keyboard commands | `chordOf`, `act`, `palette`, `focusDir`, `deliverKey`, `menu` |
| 5 | Opening tabs; Session lifetime and ownership; Background status and focus; Pane zoom (sibling status) | `closeState`, `requestClose`, `removeNode`, `endSession`, `detachedList`, `canCreate`, `renameSession`, `statuses`, `paintZoom`, `stateFor`, `command` |

### Notes for each remaining stage

- **Stage 3.** Put the fake network source in `core/` behind an interface (holds, outcomes, events, rules, decide, subscribe), with unit tests for expiry, late decisions, and save failure. Hold rows must update in place and never be rebuilt, in the list and in Monitor. The hold list is not modal and returns focus to the pane and caret it came from. Add the demo controls the mockup has for holds and reconnect.
- **Stage 4.** `neighbor()` in `core/` already gives directional focus. Chromium keeps Ctrl+Shift+T, Ctrl+Shift+W, and Ctrl+Tab, so browser tests reach those commands through the palette. Test that modified Enter reaches the pane.
- **Stage 5.** `Terminal.svelte` exists in `src/` and is used by `App.svelte`. Hosting it in a pane must not remount it on layout changes, which the flat list already ensures. A committed resize may update the PTY size, and a preview must not.

### Open questions

- **Group outline size.** The outline for a new or joined group is drawn at the group's future size, which can overlap a neighbor of the target pane. The alternative draws it on the target pane as it is now. The user has not chosen.
- **Palette of `tokens.css` and `workspace.css`.** The playground uses the mockup's colors. Reconcile them when the workspace replaces the current app in stage 5.

### Working in the sandbox

- `node_modules` is shared between the macOS host and the Linux sandbox. Do not run `npm ci` or `npm install` in the sandbox when it exists. If the build fails with "Cannot find native binding", add the Linux packages named in `package-lock.json` beside the macOS ones with `npm pack` and `tar`.
- The sandbox has no git identity. Earlier commits on this branch were made with `git -c user.name=... -c user.email=...`, using the identity of the branch's existing commits.
- Commit only when the user asks.

## Verification

- Every stage: `npm run check`, `npm test`, and `npm run test:browser` in `experiments/desktop/frontend`.
- `npm run test:browser` builds the frontend and drives the playground in headless Chromium. It needs a one-time `npx playwright install chromium-headless-shell`, and it is not part of `npm test` or `make check`. It writes screenshots to `tests/browser/output/`.
- Stage 5: `make check` in `experiments/desktop`.
- Host only, not available in the sandbox:
  - chords in a native Wails window, where no browser reserves them;
  - PTY resize on split, zoom, and divider drag with a real shell;
  - focus return to a real terminal caret;
  - WebKit-only rendering and keyboard differences, until the browser tests also run Playwright's WebKit build.
