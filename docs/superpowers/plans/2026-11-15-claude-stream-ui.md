# Claude Stream UI Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the six stream-driven improvements approved in this conversation.
**Architecture:** Keep raw capture unchanged. Interpret frames in a pure TypeScript state module, render focused Svelte components, and use the existing user-message transport for advertised slash commands.
**Tech Stack:** Svelte 5, TypeScript, Wails/Go, sanitized Markdown.
**Spec:** User-approved list: subscription/rate indicators, progress, descriptive expandable tool activity, Markdown, per-turn details/context, searchable advertised commands.

## Constraints
- Retain the existing prototype styling and permission flows.
- Missing metadata stays unknown. Reported list cost is not a bill.
- Tool completion does not establish success of the task it attempted.
- Use latest-request usage for context, not accumulated turn usage.
- Keep live logs private and untracked; synthetic fixtures only in source.

## Review Focus
- Partial/missing fields and future events must not break older CLI sessions.
- Duplicated assistant/tool frames must not duplicate transcript output.
- Interrupted and failed turns must not leave running indicators behind.
- Markdown must not execute HTML or load arbitrary remote images.
- Command filtering, keyboard selection, and insertion must preserve user control.

## Tasks
- [x] Add `frontend/src/claude-stream.ts` and synthetic unit tests for event interpretation, usage, progress, tool lifecycle, deduplication, and interruption. Replay the local live recording separately.
- [x] Add sanitized Markdown, account/usage, tool activity, and turn-summary components. Verify type checks, sanitizer behavior, and Svelte analysis.
- [x] Add command filtering/insertion in the composer and integrate components in App.svelte. Test keyboard interactions and command submission through existing SendPrompt.
- [x] Enrich demo frames with representative metadata/tool activity; verify Go tests and recording compatibility.
- [x] Run frontend tests/check/build, Go tests/race/root tests, native compilation, and browser smoke/replay. Update README with semantics and limitations.

## Verification and rulings
- Seven synthetic stream tests pass, along with Svelte checking and frontend build.
- Backend race tests, nested module server tests, root `make test`, and native purego compilation pass.
- Browser demo passed draft preservation, slash insertion/submission, progress, tool activity, usage, Markdown, and turn details.
- Replayed the local 600-record live session through both the reducer and browser bridge; no recorded commands were executed.
- Browser sanitizer checks remove scripts, images, and unsafe links; HTTP(S) links remain available.
- Small-window check verifies the composer remains within the 800×600 viewport with usage expanded.
- Review fixes: preserve drafts, recognize actual abort reasons, settle canceled permission states, announce keyboard command selection, and avoid stale permission RPC responses overwriting newer progress.
- Local command output is rendered even when no model message is produced; absent usage remains unknown.
- Svelte analyzer's raw-HTML warning is addressed by explicit DOMPurify allowlisting and the browser injection check. Existing element bindings remain intentional for scrolling/focus.
- Native WebKit launch remains host verification; only native compilation was available here.
