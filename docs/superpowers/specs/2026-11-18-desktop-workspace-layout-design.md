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

A dedicated Claude conversation could become another pane type. Its addition should require no changes to docking semantics.

## Manual docking

The drag source sets the unit of movement. A tab carries its root layout, a group handle carries the whole subtree, and a pane header carries one view. Dragging inside terminal text remains a selection gesture.

1. **Reveal.** Hovering over the center of a tab temporarily reveals its workspace. Moving through the gaps between tabs instead shows a reorder or new-tab insertion line.
   Releasing over the center of a different tab uses the right edge of its workspace as the default. Show that preview before accepting the drop.
2. **Target.** The workspace’s outer edge targets the whole tab. An edge inside a pane targets that pane. Give the outer edge priority where they overlap. Outline the selected target and draw the proposed panes with their actual titles.
3. **Orient.** At the top or bottom, the incoming group’s immediate children run left to right. At the left or right, they run top to bottom. Preserve child order and relative weights. A child that is itself a group retains its own orientation, order and proportions.
4. **Commit.** Release to apply exactly the preview in one operation. Remove an emptied source tab. If removing a child leaves a group with one child, promote that child. Preserve every group still containing multiple children. An invalid drop or Escape restores the previous layout, active tab and focus.
5. **Adjust.** Drag a divider to resize neighboring panes. A group or pane can be picked up again and docked elsewhere or moved into a tab-bar gap. Keyboard-accessible move commands must offer the same source, target, edge and new-tab choices.

**Nested example.** Dock a group containing Shell and the pair (Codex, Tests) to the right of Logs. Shell goes above the pair. If Codex and Tests were side by side inside that pair, they remain side by side. Only the two direct children of the incoming group were reoriented.

Reject a move into the source itself or one of its descendants. When the available space cannot accommodate the result at the supported pane minimums, show the target as unavailable and keep the source intact. Preview and commit must use the same sizing and targeting rules.

## Background status and focus

A terminal agent may report **Working**, **Needs input** or **Done**. A quiet PTY supplies no evidence that a turn has finished. Use agent events for a structured agent view or program-reported status for a terminal view. The architecture places terminal program status under future OSC 7501 support. The experiment can exercise these states with simulated reports.

For a mixed background tab, show **Needs input** ahead of **Working**, then **Done**. Name the contributing pane in the accessible label and status detail. When several panes need input, the detail lists them so the user can focus the right one. A finished badge stays visible until the user visits that pane or a new turn starts. An explicit error stays distinguishable from a successful finish. Status changes never steal terminal keyboard focus.

A held network request belongs to the sandbox. Surface its count in Monitor and the workspace attention UI even when the user is in another tab. Never attribute a host request to an agent by correlating timestamps. A Monitor and Logs pane in the same tab still represent one underlying request.

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
| Use + beside a mixed tab while a different tab is selected. | A new tab opens with the adjacent tab’s last-focused pane type. |
| Leave an agent running while another pane needs input and a network hold arrives. | The tab identifies the waiting pane. The hold is identifiable as sandbox-wide. Keyboard focus stays put. |
| With a detach-capable backend, close a live terminal pane and attach to its session again. | The same session is available. Explicitly ending it instead is observable from other attached views. |
| Change filters in one of two Logs views and rearrange both. | Filters remain independent. Moving either view preserves its filters and scroll position. |

### Related project documents

- [Desktop system architecture and UI design gate](https://github.com/bernd/vibepit/blob/experiment/desktop/docs/superpowers/specs/2026-11-15-desktop-architecture-design.md)
- [Agent sessions in vibed](https://github.com/bernd/vibepit/blob/experiment/desktop/docs/superpowers/specs/2026-11-15-agent-sessions-design.md)
- [Desktop experiment and current behavior](https://github.com/bernd/vibepit/tree/experiment/desktop/experiments/desktop)
