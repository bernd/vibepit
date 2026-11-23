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
- [x] **2. Workspace shell.** Playground entry point, tab bar, panes with placeholder content, group handles, dividers, zoom, pointer docking with the diagram preview, and the keyboard move dialog.
  Check: the docking, zoom, equalize, and persistence acceptance checks pass in the playground.
- [ ] **3. Held connections.** Fake network source, header readout, hold list, Monitor and Logs panes, expiry, late decisions, save failure, and connection states.
  Check: the hold acceptance checks pass, including focus return and rows that stay in place.
- [ ] **4. Commands.** Command registry, chords, command palette, **Send next key to terminal**, menus with focus return, and directional pane focus.
  Check: the keyboard acceptance checks pass. Every command in the spec's table is in the palette.
- [ ] **5. Sessions and the real terminal.** Fake session source with program state, close-pane rules, attach picker, session limit, rename, status badges, then `Terminal.svelte` inside a pane against the demo backend.
  Check: the session acceptance checks pass. `App.svelte` either hosts the workspace or is replaced by it.

## Verification

- Every stage: `npm run check`, `npm test`, and `npm run test:browser` in `experiments/desktop/frontend`.
- `npm run test:browser` builds the frontend and drives the playground in headless Chromium. It needs a one-time `npx playwright install chromium-headless-shell`, and it is not part of `npm test` or `make check`. It writes screenshots to `tests/browser/output/`.
- Stage 5: `make check` in `experiments/desktop`.
- Host only, not available in the sandbox:
  - chords in a native Wails window, where no browser reserves them;
  - PTY resize on split, zoom, and divider drag with a real shell;
  - focus return to a real terminal caret;
  - WebKit-only rendering and keyboard differences, until the browser tests also run Playwright's WebKit build.
