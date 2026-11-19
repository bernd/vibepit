# Vibepit desktop workspace layout

Design draft · 18 November 2026 · Target: `experiment/desktop`

Most work starts in a terminal running a coding agent. A developer wants to watch that agent while running tests, inspect sandbox network activity without losing their place, and see when a background agent needs attention. The desktop should make those three things possible within one project window.

## A worked example

Open a tab containing two terminal panes, **Codex** and **Tests**, in that order. Open a second tab, **Logs**, for the same sandbox. Pick up the first tab and hover over Logs. Its workspace comes into view. Moving the pointer into that workspace chooses the destination.

| Drop location | What the preview and committed layout show |
|---|---|
| Top edge | **Codex** at top left, **Tests** at top right. **Logs** spans the width below them. |
| Right edge | **Logs** fills the left. **Codex** is at top right, **Tests** below it. |
| Back in the tab bar, between titles | An insertion line moves the entire tab to that position. Its internal layout stays intact. |

After either edge drop, the destination tab contains three panes. Codex and Tests retain a common group handle. Drag that handle into the tab bar and both terminals become a separate tab in their current arrangement. Drag only the Tests header and only Tests moves. All three views retain their state throughout.

## Window, tab, group and pane

| Object | Responsibility |
|---|---|
| Window | Connects to one sandbox. Its header identifies the sandbox across all tabs. A second window can attach to the same sandbox or another one. Docking in v1 stays inside one window. |
| Tab | Occupies one slot in the window’s single tab bar. Its content is a pane or a group. Selecting a tab restores its layout and last-focused pane. |
| Group | Contains ordered panes or other groups, an orientation, and relative sizes. An explicit group remains independently movable even when its orientation matches its parent’s. |
| Pane | An individual view with a stable identity. A terminal pane attaches to a shell session. Monitor and Logs panes view the window’s sandbox network state. |

### Opening tabs

The **+** and dropdown sit together at the end of the tab bar. The dropdown offers **Session**, **Monitor**, and **Logs**. Each opens one pane in a new tab. **+** repeats the type of the tab immediately to its left, even when another tab is selected. For a mixed tab it uses the type of that tab’s last-focused pane. The tooltip and accessible name identify what will open. With an empty tab bar, **+** opens a Session.

| Choice | Initial view |
|---|---|
| Session | A fresh shell in the connected sandbox. The user may run any terminal coding agent there. Repeating this type starts a shell, regardless of which program is running next door. |
| Monitor | The desktop equivalent of `vibepit monitor`, covering held requests and their decisions, HTTP/DNS request history, host statistics, and effective rules. |
| Logs | The monitor’s network event view opened directly. Each view owns its filters, scroll position and follow state. All views observe the same underlying sandbox activity and decisions. |

**Split right** and **Split below** in any pane’s menu open a fresh shell next to that pane in the same tab. Focus moves to the new shell. The existing session or network view keeps its state. If the split cannot fit, leave the layout intact and create no session. **+** continues to open a tab. Its tooltip and accessible label update as soon as focus changes within the adjacent tab, using the same type selection as the click action.

**Rename session** gives a live shell session a descriptive name, such as *Proxy tests*. Use the name in pane headers, tab and group titles, docking previews, attention items and the reattach picker. Keep the session ID stable and propagate the name to other attached views. Ignore surrounding whitespace and require a nonempty name.

A dedicated Claude conversation could become another pane type. Its addition should require no changes to docking semantics.

## Manual docking

The drag source sets the unit of movement. A tab carries its root layout, a group handle carries the whole subtree, and a pane header carries one view. Dragging inside terminal text remains a selection gesture.

1. **Reveal.** Hovering over the center of a tab temporarily reveals its workspace. Moving through the gaps between tabs instead shows a reorder or new-tab insertion line.
   Releasing over the center of a different tab uses the right edge of its workspace as the default. Show that preview before accepting the drop.
2. **Target.** The workspace’s outer edge targets the whole tab. An edge inside a pane targets that pane. Give the outer edge priority where they overlap. Outline the selected target and draw the proposed panes with their actual titles.
3. **Orient.** At the top or bottom, the incoming group’s immediate children run left to right. At the left or right, they run top to bottom. Preserve child order and relative weights. A child that is itself a group retains its own orientation, order and proportions.
4. **Commit.** Release to apply exactly the preview in one operation. Remove an emptied source tab. If removing a child leaves a group with one child, promote that child. Preserve every group still containing multiple children. An invalid drop or Escape restores the previous layout, active tab and focus.
5. **Adjust.** Drag a divider to resize neighboring panes. A group or pane can be picked up again and docked elsewhere or moved into a tab-bar gap. Keyboard-accessible move commands must offer the same source, target, edge and new-tab choices.

**Equalize sizes** in a group’s action menu distributes space equally among its immediate children, subject to their minimum sizes. Nested groups keep their internal weights. Save the adjusted proportions with the layout.

**Nested example.** Dock a group containing Shell and the pair (Codex, Tests) to the right of Logs. Shell goes above the pair. If Codex and Tests were side by side inside that pair, they remain side by side. Only the two direct children of the incoming group were reoriented.

Reject a move into the source itself or one of its descendants. When the available space cannot accommodate the result at the supported pane minimums, show the target as unavailable and keep the source intact. Preview and commit must use the same sizing and targeting rules.

## Pane zoom

In a tab with multiple panes, **Zoom pane** expands one Session, Monitor, or Logs pane to fill the workspace below the tab bar. Keyboard focus alone selects where input goes. Zoom requires an explicit action. Keep the tab bar and workspace attention controls available while zoomed.

Show **Zoom** in the pane header and **Restore layout** in the same position while zoomed. **Ctrl+Shift+Enter** (Command+Shift+Enter on macOS) toggles the focused pane. Restore brings back the exact group hierarchy, pane order and divider proportions, with keyboard focus in the same pane. Hidden siblings continue running. Zoom does not create, detach, or replace a view.

Zoom is temporary window state. Switching tabs or committing a layout move returns to the tiled view. A drag previews the full layout, and canceling it restores the zoomed view. Single-pane tabs already use the available workspace.

## Background status and focus

A terminal agent may report **Working**, **Needs input** or **Done**. A quiet PTY supplies no evidence that a turn has finished. Use agent events for a structured agent view or program-reported status for a terminal view. The architecture places terminal program status under future OSC 7501 support. The experiment can exercise these states with simulated reports.

For a mixed background tab, show **Needs input** ahead of **Working**, then **Done**. Name the contributing pane in the accessible label and status detail. When several panes need input, the detail lists them so the user can focus the right one. A finished badge stays visible until the user visits that pane or a new turn starts. An explicit error stays distinguishable from a successful finish. Status changes never steal terminal keyboard focus.

A held network request belongs to the sandbox. Surface its count in Monitor and the workspace attention UI even when the user is in another tab. Never attribute a host request to an agent by correlating timestamps. A Monitor and Logs pane in the same tab still represent one underlying request.

Show each hold’s remaining time in Monitor. At the proxy deadline, withdraw its decision controls and attention item, refresh the count in all views, and record the expiry in Logs. Apply a decision only to its still-pending request ID. A new request for the same host has a separate ID. The mockup uses a 20-second deadline so expiry can be exercised without a proxy.

## Session lifetime and ownership

Moving, tiling, resizing or hiding a pane is a presentation change. Preserve its terminal scrollback and selection, network filters, or agent draft. Keep its attachment and client identity stable through a move. A committed terminal resize may update the PTY dimensions.

**Close pane** removes that view. A sandbox-managed terminal or agent session detaches and remains discoverable by session ID for later reattachment. **End session** explicitly stops the named session and affects its other attached clients. Closing a Monitor or Logs pane releases its view. The window’s network feature owns subscriptions and pending decisions. Closing the last pane returns to the connected window’s empty state with **+** available.

Closing a whole tab applies Close pane to each of its views. Save committed tab order, layout trees, weights, last focus, and session references in desktop preferences for the sandbox and window. A temporary hover preview never enters those preferences.

The current experiment also has a Claude process bound to its SSH channel. Durable agent detach and reattach depend on the planned vibed session service. The layout experiment can proceed against simulated session state. The live close-and-reattach check applies when the relevant session service supports it. Until then, name a process-ending action explicitly and exercise the detach path in the demo. Implementing durable sessions belongs to the sandbox service work. During reconnect, preserve the layout, reattach by ID, and show an exited or unavailable session in place if it cannot be restored.

| State | Owner |
|---|---|
| Tab order, stable pane and group IDs, layout tree, weights and last-focused pane | Desktop workspace layout |
| Terminal and agent sessions, attachment roles and reported process state | Sandbox services, surfaced through the corresponding desktop feature |
| Holds, network history, host statistics and effective rules | Sandbox proxy, accessed through the host’s network control client |
| Network filters, terminal selection and other view-local state | Individual pane or its feature |

### Reconnect with an exited terminal

Alongside successful reconnect, provide a demo scenario that exits one selected terminal during the interruption. Restore the layout and other attachments. The affected pane shows **Exited**, retains its transcript, and offers **Open replacement shell**. This explicit action starts a fresh session in the same pane. All group memberships, divider positions, and sibling views survive.

## Alternatives and scope

The per-pane tab-bar sketch let each column switch independently. The selected single bar gives the whole arrangement one place to return to and one place to show background status. The automatic grid sketch made adding a fourth pane convenient, but the drop edge expresses an intention the grid cannot infer. V1 uses manual docking with a live preview.

This document covers the workspace layout portion of the desktop UI design. The sandbox picker, approval and diff review flows, native notifications, settings, and driver-specific agent screens belong in companion designs. Hover timing, hit-zone dimensions, minimum pane sizes, and shortcut bindings are prototype tuning decisions. A visible group outline and keyboard focus indication must communicate the target independently of color.

## Acceptance checks

| Exercise | Expected result |
|---|---|
| Drop Codex + Tests above Logs, then repeat from the same starting state at the right edge. | The two results match the worked example, including labels and order. A preview causes no structural edit. |
| Move the pair back to the tab bar, then repeat with only Tests. | The first move carries both panes. The second carries one. The terminal sessions keep their identities and continue running. |
| Dock the nested Shell + (Codex, Tests) group. | Only Shell and the pair change orientation relative to each other. Explicit child groups survive. |
| Hover a background tab, switch between outer and pane targets, then cancel. | The outline and preview identify the affected region. Cancellation restores selection, layout and focus. |
| Repeat a move using keyboard commands. | The same layouts and new-tab destinations are reachable. |
| Split a Logs pane right, then split its shell below. Rename the new session and move it. | New shells open in place and receive focus. The name follows the same session in headers, previews, attention and reattachment. |
| Change focus between Logs and a terminal in the rightmost tab. | The + tooltip and accessible name immediately describe the type that clicking it opens. |
| Resize a nested group, then equalize its parent. | The parent’s children share the space and the nested proportions survive. |
| Let a simulated network hold expire while viewing it in two windows. | Both lose the hold and its actions. Logs show one expiry and the rules are unchanged. |
| Reconnect with one terminal exited. | The exited terminal remains in place. Opening a replacement there preserves the layout and other sessions. |
| Use + beside a mixed tab while a different tab is selected. | A new tab opens with the adjacent tab’s last-focused pane type. |
| Leave an agent running while another pane needs input and a network hold arrives. | The tab identifies the waiting pane. The hold is identifiable as sandbox-wide. Keyboard focus stays put. |
| With a detach-capable backend, close a live terminal pane and attach to its session again. | The same session is available. Explicitly ending it instead is observable from other attached views. |
| Resize a divider in a nested group, zoom a terminal with an unsent command, then restore. Repeat with a Logs pane. | Only the requested pane fills the workspace. The original group tree, divider proportions, draft and filters survive. The same pane retains keyboard focus. |
| Change filters in one of two Logs views and rearrange both. | Filters remain independent. Moving either view preserves its filters and scroll position. |

### Related project documents

- [Desktop system architecture and UI design gate](https://github.com/bernd/vibepit/blob/experiment/desktop/docs/superpowers/specs/2026-11-15-desktop-architecture-design.md)
- [Agent sessions in vibed](https://github.com/bernd/vibepit/blob/experiment/desktop/docs/superpowers/specs/2026-11-15-agent-sessions-design.md)
- [Desktop experiment and current behavior](https://github.com/bernd/vibepit/tree/experiment/desktop/experiments/desktop)

## Open review findings

Review of 19 November 2026. Each item still needs a decision or a fix in this document. #1, #2 and #5 change behavior the acceptance checks depend on and come first.

### Conflicts with the architecture and code

1. **Windows per sandbox.** This document lets a second window attach to the same sandbox and has an acceptance check that uses two windows. The architecture design (Desktop backend, Workspaces) allows one window per sandbox and focuses the existing window instead, so each sandbox has one connection, one attention list and one set of notifications. Choose one rule. If multiple windows stay, the architecture must say which window owns attention, notifications and the holding indicator, and what "preferences for the sandbox and window" are keyed by.
2. **Hold decisions are per target, not per request ID.** The proxy keys holds by target (`proxy/hold.go`, `held map[Target]*hold`). Approving adds a runtime allow rule and then ends matching holds (`EndAllowed`). Rejecting adds the target to the deny set and ends its hold. Nothing checks that a hold ID is still open, so a late decision on an expired hold still changes the rules. "Apply a decision only to its still-pending request ID" and "the rules are unchanged" need either a conditional decision in the control API (decide hold ID N, fail if it has ended) or a weaker promise.
3. **Expiry in Logs.** On expiry the proxy does not append a log entry. It sets the outcome of the hold's existing entry to `timeout` and publishes `hold-end`. "Record the expiry in Logs" and "Logs show one expiry" should describe the hold row changing. One hold can cover several requests to the same target, so "one underlying request" should read "one underlying hold", and a new request gets a separate ID only after the earlier hold has ended.
4. **Exited terminal transcript.** `session.Manager` removes a session once its process exits (`session/manager.go`). After reconnect, the transcript of an **Exited** pane can only come from the desktop's in-memory terminal buffer. That works while the window stayed open, but not after the desktop restarts. Name the source and state what the pane shows without one.

### Undefined tree operations

5. **Explicit groups and merging.** "Explicit group" is never defined, and the spec does not say whether a group created by docking at a pane edge is explicit. Docking C at the right outer edge of a side-by-side root (A, B) could give (A, B, C) or ((A, B), C). Docking C right of pane A inside (A, B) could give (A, C, B) or ((A, C), B). If every dock creates a permanent group, trees get deeper with each move and group handles multiply. Proposal: groups created by docking are implicit and merge into a parent of the same orientation. Only groups that were moved in stay explicit.
6. **Size of the incoming content.** "Preserve relative weights" covers the incoming group's own children, but not how much of the target it takes (half, its previous size, something else). Preview and commit must agree, so this needs a rule rather than prototype tuning.
7. **Active tab and focus after a commit.** Escape and an invalid drop restore them. A successful drop does not say which tab becomes active or which pane gets focus.
8. **Round trips change the moved group's orientation.** Moving into a tab-bar gap keeps the internal layout. Docking at an edge changes the orientation of the moved group's direct children, so docking a side-by-side pair at a right edge and dragging it back to the tab bar returns it stacked. State that this is intended, and decide whether a modifier keeps the original orientation.

### Gaps in behavior

9. **Detached shells and the session limit.** **Close pane** detaches, while Split and **+** always start fresh shells. vibed allows 50 sessions (`cmd/vibed.go`), so they pile up, and the spec does not say what happens at the limit. "The reattach picker" is referenced but never designed. The **+** dropdown offers only Session, Monitor and Logs. Design the picker or a detached-sessions list. Also consider whether closing a pane whose shell sits idle at a prompt should end the shell, since that is what desktop terminal users expect.
10. **Attention while zoomed.** Status badges are defined for background tabs. A sibling hidden by zoom in the current tab can need input with no visible signal. Show sibling status while zoomed, for example on **Restore layout**.
11. **Holding stays on after closing Monitor.** The window's network feature owns the subscription, so holding stays on while the window is connected, even with every Monitor and Logs pane closed. The architecture requires a holding indicator, and this document should place it, for example in the window header or attention UI.
12. **Several views of one session.** Each view has its own client ID and only one is the writer. Describe the read-only view and the **Take back input** banner from the architecture's terminal service section.
13. **Status precedence.** The order Needs input, Working, Done leaves out Error and Exited, which the architecture treats as attention items. Place them and map the states to OSC 7501 (`blocked` becomes Needs input, with its `kind`).

### Design questions

14. **What + opens.** Because **+** sits at the end of the bar, "the tab immediately to its left" always means the last tab. That rule makes the focus-tracking tooltip and two acceptance checks necessary. "**+** opens a Session, the dropdown opens anything else" removes all three. Prefer that unless the prototype shows people want the repeat.
15. **Shortcuts taken from terminals.** State which key chords the desktop takes from terminal panes. Terminal coding agents rely on modified Enter keys.
16. **Session name validation.** Besides trimming whitespace and requiring a nonempty name, vibed's `rename` should limit length and allow only printable characters, as with `VIBEPIT_LABEL`. Names appear in attention items and other clients.

### Acceptance checks to add or change

- Quit and reopen the desktop. Tab order, layout tree, weights and last focus are restored, and no hover preview was saved.
- Reach the session limit with Split and **+**.
- Rewrite the two-window hold expiry check once #1 is decided.

### Editorial

- Use relative links for the related documents instead of absolute `blob/experiment/desktop` URLs.
- "Keyboard focus alone selects where input goes. Zoom requires an explicit action." could read "Focusing a pane never zooms it."
