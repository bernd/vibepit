# Desktop Shell Implementation Plan

> **For agentic workers:** Use independent native/frontend tasks and backend implementation with integration verification.

**Goal:** Add tray presence, native notifications, and independent project windows to the authorized throwaway prototype.
**Architecture:** An application-owned Workspaces registry owns one existing Service per window. Closing a native window hides its webview and preserves connections; Quit disconnects all. Native shell hooks implement window creation, tray, and notifications. Browser preview exercises window scoping with separate browser windows.
**Tech Stack:** Wails 3.1, Go, Svelte 5, TypeScript.
**Spec:** User-selected features from preceding discussion: tray, notifications, multiple windows.

## Constraints and review focus
- Route every RPC/event to its workspace; reject cross-window connection tokens.
- Never hold registry locks across Service calls, native calls, or emitted callbacks.
- Preserve hidden-window transcript and connection; disconnect only on explicit disconnect/quit.
- Notifications focus the relevant window and never approve a request. Withdraw on resolution, cancellation, disconnection, or expiry; avoid duplicate SSE notices.
- Native permission/notification unavailability must not disable terminal/agent functionality.
- Browser preview remains demo-only and never attempts OS tray/notification APIs.
- Reloading a connected browser view needs explicit recovery, never silent agent termination.

## Tasks
- [x] Backend registry, event summaries, notification lifecycle, scoped RPC facade, and tests for two simultaneous windows and shutdown.
- [x] Native shell: tray, per-window lifecycle, notification clicks/withdrawal, graceful errors, shutdown.
- [x] Svelte scoped bridge, new/hide window and notification controls, browser popup/reload behavior.
- [x] Integration: Go/race/frontend/root tests, native compilation, two-window browser smoke, documentation, final review.

Native launch requires host WebKit/tray/notification environment; compile and browser preview are available here.

## Verification

- `make check` in the desktop module: frontend build/type checks, seven frontend tests, server and native purego Go tests passed.
- `go test -race -tags purego ./...` passed; native Linux purego build passed.
- Root `make test` and `make test-integration` passed.
- Two-window browser smoke passed: distinct workspace IDs, concurrent YOLO/normal conversations, independent disconnect, continued second-window turns, explicit reload recovery and reconnect, no browser errors.
- Focused review findings resolved: Linux D-Bus tray refresh, Linux left-click restore, unavailable-tray minimization with accessible Quit, and authoritative validity checks for delayed notification callbacks.
- Native tray/OS notifications and live container attachment still require host verification; runtime dependencies are unavailable in this sandbox. Windows toast withdrawal limitation documented in the experiment README.
