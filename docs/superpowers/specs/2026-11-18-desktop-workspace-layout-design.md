# Vibepit desktop workspace layout

Design draft · 18 November 2026 · Target: `experiment/desktop`

Most work starts in a terminal running a coding agent. A developer wants to watch that agent while running tests, inspect sandbox network activity without losing their place, and see when a background agent needs attention. The desktop should make those three things possible within one project window.

## A worked example

Open a tab containing two terminal panes, **Codex** and **Tests**, in that order. Open a second tab, **Logs**, for the same sandbox. Pick up the first tab and hover over Logs. A diagram of its workspace appears. Moving the pointer over that diagram chooses the destination.

| Drop location | What the preview and committed layout show |
|---|---|
| Top edge | **Codex** at top left, **Tests** at top right. **Logs** spans the width below them. |
| Right edge | **Logs** fills the left. **Codex** is at top right, **Tests** below it. |
| Back in the tab bar, between titles | An insertion line moves the entire tab to that position. Its internal layout stays intact. |

After either edge drop, the destination tab contains three panes. Codex and Tests retain a common group handle. Drag that handle into the tab bar and both terminals become a separate tab in their current arrangement. Drag only the Tests header and only Tests moves. All three views retain their state throughout.

## Window, tab, group and pane

| Object | Responsibility |
|---|---|
| Window | Connects to one sandbox. Its header identifies the sandbox across all tabs and carries the hold readout. A sandbox has one window in a desktop instance, and opening that sandbox again focuses its window. Another window connects to another sandbox. Docking in v1 stays inside one window. |
| Tab | Occupies one slot in the window’s single tab bar. Its content is a pane or a group. Selecting a tab restores its layout and last-focused pane. |
| Group | Contains ordered panes or other groups, an orientation, and relative sizes. A group is *explicit* when the user moved it in as a unit, and *implicit* when a dock or split created it. An explicit group remains independently movable even when its orientation matches its parent’s. The root group of a tab shows no handle, because the tab is its handle. |
| Pane | An individual view with a stable identity. A terminal pane attaches to a shell session. Monitor and Logs panes view the window’s sandbox network state. |

### Opening tabs

The **+** and dropdown sit together at the end of the tab bar. **+** opens a Session in a new tab. The dropdown offers **Session**, **Monitor**, **Logs**, and **Attach session…**. Each opens one pane in a new tab.

| Choice | Initial view |
|---|---|
| Session | A fresh shell in the connected sandbox. The user may run any terminal coding agent there. |
| Monitor | The desktop equivalent of `vibepit monitor`, covering held requests and their decisions, HTTP/DNS request history, host statistics, and effective rules. |
| Logs | The monitor’s network event view opened directly. Each view owns its filters, scroll position and follow state. All views observe the same underlying sandbox activity and decisions. |
| Attach session… | A picker of the sandbox’s detached sessions, each with its name, ID, age, and an **End session** action. Choosing one attaches it in a new tab. |

**Split right** and **Split below** in any pane’s menu open a fresh shell next to that pane in the same tab. The new shell takes half of that pane’s space. Focus moves to the new shell. The existing session or network view keeps its state. If the split cannot fit, leave the layout intact, create no session, and say why in a status message that stays until the next action.

**Rename session** gives a live shell session a descriptive name, such as *Proxy tests*. Use the name in pane headers, tab and group titles, docking previews, attention items and the attach picker. Keep the session ID stable and propagate the name to other attached views. Ignore surrounding whitespace. Require one to 64 printable characters. vibed applies the same rule, because names reach other clients.

A new session is named *Shell* with the lowest number unused in the sandbox, such as *Shell 2*. A tab or group title joins the names of its panes with a plus sign. Beyond two panes, it shows the first name and a count, such as *Codex + 2*. Truncate a long title in the tab bar and keep the full title in the tooltip and accessible name.

vibed limits the number of sessions. At the limit, **+** and Split create nothing, name the limit, and offer the attach picker so the user can end sessions.

A dedicated Claude conversation could become another pane type. Its addition should require no changes to docking semantics.

## Manual docking

The drag source sets the unit of movement. A tab carries its root layout, a group handle carries the whole subtree, and a pane header carries one view. Dragging inside terminal text remains a selection gesture.

1. **Reveal.** Hovering over the center of a tab shows a diagram of its workspace in place of the current one. The diagram draws each pane with its actual title, and it is the same one the keyboard move command shows. Live pane content stays untouched until the drop. A tab becomes the target only after the pointer rests on it for a moment, so crossing other tabs on the way to the workspace does not retarget. Moving through the gaps between tabs instead shows a reorder or new-tab insertion line.
   Releasing over the center of a different tab uses the right edge of its workspace as the default. Show that preview before accepting the drop.
2. **Target.** The workspace’s outer edge targets the whole tab. An edge inside a pane targets that pane. Give the outer edge priority where they overlap. Inside a pane, an edge is chosen only while the pointer is in the outer third on that side. In the middle the preview keeps its last edge, and near a corner it keeps the current edge until another is clearly closer, so a small movement never flips the preview. Outline the selected target and draw the proposed panes with their actual titles.
3. **Orient.** At the top or bottom, the incoming group’s immediate children run left to right. At the left or right, they run top to bottom. Preserve child order and relative weights. A child that is itself a group retains its own orientation, order and proportions.
4. **Commit.** Release to apply exactly the preview in one operation. Remove an emptied source tab. If removing a child leaves a group with one child, promote that child. Preserve every group still containing multiple children. The destination tab becomes active, and focus goes to the moved pane, or to the last-focused pane of a moved group. An invalid drop, a release outside the workspace and tab bar, or Escape restores the previous layout, active tab and focus.
5. **Adjust.** Drag a divider to resize neighboring panes. A group or pane can be picked up again and docked elsewhere or moved into a tab-bar gap. Keyboard-accessible move commands must offer the same source, target, edge and new-tab choices.

**Equalize sizes** in a group’s action menu distributes space equally among its immediate children, subject to their minimum sizes. Nested groups keep their internal weights. Save the adjusted proportions with the layout.

**Nested example.** Dock a group containing Shell and the pair (Codex, Tests) to the right of Logs. Shell goes above the pair. If Codex and Tests were side by side inside that pair, they remain side by side. Only the two direct children of the incoming group were reoriented.

**Size.** Incoming content takes half of its target, which is the pane for a pane edge and the whole tab for an outer edge. The existing content keeps its relative weights in the other half. Preview and commit use this rule.

**Merging.** An implicit group merges into a parent of the same orientation. Docking C at the right outer edge of a side-by-side root (A, B) gives (A, B, C). Docking C to the right of A inside (A, B) gives (A, C, B). Docking C below A gives a stacked implicit group (A, C) beside B. An explicit group never merges, and it stays explicit until promotion leaves it with one child. Every group except the root shows a handle.

**Round trips.** Docking at an edge reorients the moved group’s direct children, while a move into a tab-bar gap keeps the internal layout. A side-by-side pair docked at a right edge and dragged back to the tab bar therefore returns stacked. This is intended. V1 has no modifier that keeps the original orientation.

Mark the panes being moved while a drag is in progress. Over the source itself or one of its descendants, show no preview; a release there leaves the layout unchanged. When the available space cannot accommodate the result at the supported pane minimums, show the target as unavailable and keep the source intact. Preview and commit must use the same sizing and targeting rules.

## Pane zoom

In a tab with multiple panes, **Zoom pane** expands one Session, Monitor, or Logs pane to fill the workspace below the tab bar. Focusing a pane never zooms it. Keep the tab bar and the header’s hold readout available while zoomed.

Show **Zoom** in the pane header and **Restore layout** in the same position while zoomed. **Ctrl+Shift+M** (Command+Shift+M on macOS) toggles the focused pane. Restore brings back the exact group hierarchy, pane order and divider proportions, with keyboard focus in the same pane. Hidden siblings continue running. Zoom does not create, detach, or replace a view. While zoomed, **Restore layout** carries the highest status among the hidden siblings and names those panes in its accessible label.

Zoom is temporary window state. Switching tabs or committing a layout move returns to the tiled view. A drag previews the full layout, and canceling it restores the zoomed view. Splitting or closing the zoomed pane also returns to the tiled view. A zoomed terminal that exits stays zoomed and shows **Exited**. Single-pane tabs already use the available workspace.

## Background status and focus

A terminal agent may report **Working**, **Needs input** or **Done**. A quiet PTY supplies no evidence that a turn has finished. Use agent events for a structured agent view or program-reported status for a terminal view. The architecture places terminal program status under future OSC 7501 support. The experiment can exercise these states with simulated reports.

For a mixed background tab, show the highest of **Needs input**, **Error**, **Exited**, **Working**, and **Done**, in that order. With OSC 7501, `blocked` becomes Needs input and carries its `kind` (permission, question, or auth) into the status detail, `working` becomes Working, `done` becomes Done, and `idle` shows no badge. Name the contributing pane in the accessible label and status detail. When several panes need input, the detail lists them so the user can focus the right one. A finished badge stays visible until the user visits that pane or a new turn starts. An explicit error stays distinguishable from a successful finish. Status changes never steal terminal keyboard focus.

## Held connections

A held connection belongs to the sandbox, so the window header shows it. The workspace never moves or resizes for a hold, and no PTY changes size. Never attribute a hold to an agent by correlating timestamps, and put no network mark on agent panes.

### Header readout

At rest, the readout shows the holding indicator: this window’s subscription turns holding on for the sandbox, even with every Monitor and Logs pane closed. With holds pending, it shows the one with the earliest deadline as host and port with its remaining time, such as *api.github.com:443 · blocks in 17s*, and a count of the others. Panes that need input, report an error, or have exited show as a count beside it.

When a hold arrives, the readout changes and a polite live region announces the host and deadline once. Keyboard focus stays where it was. The hold list stays closed until the user opens it.

### Hold list

A click on the readout or **Ctrl+Shift+H** (Command+Shift+H on macOS) opens a list anchored under it, over the right edge of the workspace. The list is not modal. Focus moves to the first hold. Other attention items follow the holds, and selecting one focuses its pane.

Up and Down move between holds. Each hold offers the four choices of `vibepit monitor`, with the same keys and a visible button for each. Every button states its scope. The word *session* names a shell here, so the scope text avoids it.

| Key | Action | Scope shown |
|---|---|---|
| `a` | Allow | Until the sandbox stops |
| `A` | Allow and save | Saved to `.vibepit/network.yaml` |
| `n` | Reject | Until the sandbox stops. A later allow overrides it. |
| `N` | Deny and save | Saved. Wins over any allow. |

After a decision, focus moves to the next hold. When the last hold is settled, or on Escape, the list closes and focus returns to the pane and caret it came from.

Key rows by target and keep them in arrival order. A settled or expired row stays in place as a result line until the list closes, so no control moves under the pointer or loses keyboard focus. Monitor remains the detail view and shows the same holds. A decision in the list, in Monitor, or in another client such as `vibepit monitor` ends the hold everywhere.

V1 has no batch decision.

### Expiry and late decisions

A hold is keyed by its target and can cover several requests to that host and port. A new request to the same target opens a new hold only after the earlier one has ended.

At the proxy deadline, withdraw the row’s decision controls in the list and in Monitor, and refresh the readout. The row reads *Blocked after 20s* and links to the host in Monitor. In Logs, the hold’s existing row changes its outcome to timeout. The proxy appends no second row. The mockup uses a 20-second deadline so expiry can be exercised without a proxy.

A decision is a rule on the target. If it reaches the proxy after the hold has expired, the rule still applies. The feedback then says that the host is allowed or denied for later requests and that the held request had already been blocked.

### Failures and connection loss

When saving fails, say that the runtime decision stands and that nothing was saved. While the window is reconnecting or the connection is lost, withdraw all holds, disable decision controls, and show the connection state in the readout.

## Keyboard commands

Every workspace command is reachable without a pointer. Frequent commands have a chord. The command palette lists every command with its chord and acts on the focused pane, so the remaining commands need no chord.

| Command | Linux and Windows | macOS |
|---|---|---|
| Command palette | Ctrl+Shift+P | Command+Shift+P |
| Open hold list | Ctrl+Shift+H | Command+Shift+H |
| Focus pane left, right, up, down | Ctrl+Shift+Arrow | Command+Option+Arrow |
| Next and previous tab | Ctrl+Tab, Ctrl+Shift+Tab | Ctrl+Tab, Ctrl+Shift+Tab |
| Select tab 1 to 9 | Ctrl+Shift+1 to 9 | Command+1 to 9 |
| New Session tab | Ctrl+Shift+T | Command+T |
| Split right | Ctrl+Shift+D | Command+D |
| Split below | Ctrl+Shift+E | Command+Shift+D |
| Zoom pane, restore layout | Ctrl+Shift+M | Command+Shift+M |
| Close pane | Ctrl+Shift+W | Command+W |
| Hide window | Ctrl+Shift+Alt+W | Command+Shift+W |

The palette alone offers **Rename session**, **Move pane**, **Move to new tab**, **Equalize sizes**, **Close tab**, **End session**, **Attach existing session**, **Open Monitor**, **Open Logs**, and **Send next key to terminal**.

### Keys taken from terminal programs

The desktop takes the chords in the table, together with the experiment’s clipboard, find, and window chords. **Send next key to terminal** passes one of them through to the program in the focused pane.

While a terminal has focus, the desktop never takes a modified Enter, Ctrl with a letter, an Alt chord, a function key, Tab, or Escape. Terminal coding agents rely on modified Enter keys. A lone **?** also reaches the terminal, so the palette replaces the mockup’s guide shortcut.

Ctrl+Shift+Arrow is the likeliest conflict. xterm-style terminals can report it to programs, and some tmux and Emacs setups bind it. Alt+Arrow would conflict with word movement in shells.

### Focus

Tab belongs to the terminal, so chords and the palette are the way out of a terminal pane. A menu, dialog, palette, or hold list returns focus to the element it was opened from. Directional focus follows the visible layout. With a zoomed pane it does nothing.

## Session lifetime and ownership

Moving, tiling, resizing or hiding a pane is a presentation change. Preserve its terminal scrollback and selection, network filters, or agent draft. Keep its attachment and client identity stable through a move. A committed terminal resize may update the PTY dimensions.

**Close pane** on a Monitor or Logs pane releases its view. The window’s network feature owns subscriptions and pending decisions.

**Close pane** on a terminal behaves like a desktop terminal:

| Terminal state | Result |
|---|---|
| The shell is idle at its prompt and no other client is attached | The session ends and the pane closes, without a question. |
| A program is running, another client is attached, or the state is unknown | A dialog names the session and offers **End session**, **Detach**, and **Cancel**. Cancel has initial focus. |
| The session has already exited | The pane closes. |

The shell reports its prompt through shell integration (OSC 133). Without that report the state is unknown. **Detach** leaves the session running and discoverable in the attach picker by name and ID. **End session** stops the named session and affects its other attached clients. The pane menu and the palette also offer **End session** and **Detach** directly. Closing the last pane returns to the connected window’s empty state, which offers Session, Monitor, and Attach session.

**Several views of one session.** Each view attaches with its own client ID, and a session has one writer. A view that is not the writer is read-only. It shows a banner naming the current writer’s label, with **Take back input**, as the architecture’s terminal service describes.

Closing a whole tab applies Close pane to each of its views. If any of them would ask, one dialog lists those sessions and offers the same three choices for all of them. Save committed tab order, layout trees, weights, last focus, and session references in desktop preferences for the sandbox. A temporary hover preview never enters those preferences.

The current experiment also has a Claude process bound to its SSH channel. Durable agent detach and reattach depend on the planned vibed session service. The layout experiment can proceed against simulated session state. The live close-and-reattach check applies when the relevant session service supports it. Implementing durable sessions belongs to the sandbox service work. During reconnect, preserve the layout, reattach by ID, and show an exited or unavailable session in place if it cannot be restored.

**Connection states.** The header shows connecting, connected, reconnecting, lost, or unsupported, with text or shape as well as color. Until the window is connected, panes keep their content, terminal input is disabled, and **+**, Split, and network decisions are unavailable.

| State | Owner |
|---|---|
| Tab order, stable pane and group IDs, layout tree, weights and last-focused pane | Desktop workspace layout |
| Terminal and agent sessions, attachment roles and reported process state | Sandbox services, surfaced through the corresponding desktop feature |
| Holds, network history, host statistics and effective rules | Sandbox proxy, accessed through the host’s network control client |
| Network filters, terminal selection and other view-local state | Individual pane or its feature |

### Reconnect with an exited terminal

Alongside successful reconnect, provide a demo scenario that exits one selected terminal during the interruption. Restore the layout and other attachments. The affected pane shows **Exited**, retains its transcript, and offers **Open replacement shell**. vibed removes a session when its process exits, so the transcript comes from the desktop’s in-memory terminal buffer. After a desktop restart there is none, and the pane says that the session exited and its output is no longer available. This explicit action starts a fresh session in the same pane. All group memberships, divider positions, and sibling views survive.

## Alternatives and scope

The per-pane tab-bar sketch let each column switch independently. The selected single bar gives the whole arrangement one place to return to and one place to show background status. The automatic grid sketch made adding a fourth pane convenient, but the drop edge expresses an intention the grid cannot infer. V1 uses manual docking with a live preview.

This document covers the workspace layout portion of the desktop UI design. The sandbox picker, approval and diff review flows, native notifications, settings, and driver-specific agent screens belong in companion designs. Hover timing, hit-zone dimensions, and minimum pane sizes are prototype tuning decisions. User-configurable key bindings belong to the settings design. A visible group outline and keyboard focus indication must communicate the target independently of color.

## Acceptance checks

| Exercise | Expected result |
|---|---|
| Drop Codex + Tests above Logs, then repeat from the same starting state at the right edge. | The two results match the worked example, including labels and order. A preview causes no structural edit. |
| Move the pair back to the tab bar, then repeat with only Tests. | The first move carries both panes. The second carries one. The terminal sessions keep their identities and continue running. |
| Dock the nested Shell + (Codex, Tests) group. | Only Shell and the pair change orientation relative to each other. Explicit child groups survive. |
| Hover a background tab, switch between outer and pane targets, then cancel. | The outline and preview identify the affected region. Cancellation restores selection, layout and focus. |
| Drag a pane across another tab without stopping, then into the workspace. Move the pointer slightly in the middle of a pane. | The crossed tab does not become the target. The preview stays the same until the pointer pushes toward a side. |
| Repeat a move using keyboard commands. | The same layouts and new-tab destinations are reachable. |
| Split a Logs pane right, then split its shell below. Rename the new session and move it. | New shells open in place and receive focus. The name follows the same session in headers, previews, attention and reattachment. |
| Resize a nested group, then equalize its parent. | The parent’s children share the space and the nested proportions survive. |
| Let a simulated network hold expire with the hold list and a Monitor pane open, and `vibepit monitor` attached as a second client. | All three withdraw its controls. The list row stays in place and reads as blocked. The hold’s Logs row changes to timeout and the rules are unchanged. |
| Reconnect with one terminal exited. | The exited terminal remains in place. Opening a replacement there preserves the layout and other sessions. |
| Leave an agent running while another pane needs input and a network hold arrives. | The tab identifies the waiting pane. The header readout names the held host and its remaining time. The hold list stays closed and keyboard focus stays put. |
| With a detach-capable backend, close a terminal pane that runs a program, choose **Detach**, and attach to its session again. | The same session is available in the attach picker. Explicitly ending it instead is observable from other attached views. |
| Resize a divider in a nested group, zoom a terminal with an unsent command, then restore. Repeat with a Logs pane. | Only the requested pane fills the workspace. The original group tree, divider proportions, draft and filters survive. The same pane retains keyboard focus. |
| Type a partial command in a terminal, open the hold list with its chord, allow the hold with `a`, and let the list close. | The request proceeds. Focus returns to the same pane with the command and caret intact. No pane changed size. |
| With three holds listed, let the first expire while the pointer rests on a button of the second. | No row moves. The first row becomes a result line without controls. |
| Send a decision that reaches the proxy after its hold expired. | The rule applies. The feedback says the held request had already been blocked. |
| Receive a hold with a screen reader running. | The host and deadline are announced once. The countdown is not announced each second. |
| Close every Monitor and Logs pane. | The readout still shows that holding is on. A new hold appears there and can be answered. |
| In a terminal running a coding agent, press Shift+Enter, Ctrl+Enter, and Ctrl+Shift+Enter. | Each reaches the program. The layout does not change. |
| From a terminal pane, focus each neighbor with the directional chords, split right, and close the new pane, without the pointer. | Focus follows the visible layout. The new shell was idle, so closing it ends the session without a question. |
| Open the palette from a terminal with a partial command, run **Rename session**, and confirm. | The name changes. Focus returns to the same pane with the command intact. |
| Run **Send next key to terminal**, then press Ctrl+Shift+D. | The program receives the chord. No split opens. |
| Close a pane whose shell is idle, then one that runs a program, then one whose shell reports no prompt state. | The first ends without a question. The others ask, with Cancel focused. Cancel leaves the pane and session untouched. |
| Dock C at the right outer edge of side-by-side (A, B), then dock D below A. | The root holds A, B, C with no nested group, and C has half the width. A and D form one stacked group beside B and C. |
| Click **+** with a Monitor tab last in the bar. | A Session opens in a new tab. |
| Reach the session limit with Split and **+**. | Nothing is created. The message names the limit and offers the attach picker. |
| Zoom a pane, then let a hidden sibling need input. | **Restore layout** shows Needs input and names the pane. |
| Quit and reopen the desktop. | Tab order, layout tree, weights and last focus are restored. No hover preview was saved. |
| Change filters in one of two Logs views and rearrange both. | Filters remain independent. Moving either view preserves its filters and scroll position. |

### Related project documents

- [Desktop system architecture and UI design gate](2026-11-15-desktop-architecture-design.md)
- [Agent sessions in vibed](2026-11-15-agent-sessions-design.md)
- [Desktop experiment and current behavior](../../../experiments/desktop/)

## Review findings

Review of 19 November 2026. All sixteen findings were resolved in this document on 23 November 2026. They keep their numbers for reference.

| # | Finding | Resolution |
|---|---|---|
| 1 | Windows per sandbox | One window per sandbox, as in the architecture. |
| 2 | Hold decisions are per target | A decision is a rule on the target, and a late one still applies. No conditional decision is planned for the control API. |
| 3 | Expiry in Logs | Expiry changes the hold’s existing row. The text speaks of one hold per target. |
| 4 | Exited terminal transcript | The transcript comes from the desktop’s terminal buffer. The pane says so when there is none. |
| 5 | Explicit groups and merging | Implicit groups merge into a parent of the same orientation. Moved-in groups are explicit. |
| 6 | Size of the incoming content | Half of the target. |
| 7 | Active tab and focus after a commit | The destination tab, and the moved pane or the moved group’s last-focused pane. |
| 8 | Round trips change orientation | Intended. No modifier in v1. |
| 9 | Detached shells and the session limit | Closing an idle shell ends it, and a busy one asks. The dropdown has an attach picker. The limit refuses with a message. |
| 10 | Attention while zoomed | **Restore layout** carries sibling status. |
| 11 | Holding stays on after closing Monitor | The header readout carries the holding indicator. |
| 12 | Several views of one session | Read-only views show the writer and **Take back input**. |
| 13 | Status precedence | Needs input, Error, Exited, Working, Done, with the OSC 7501 mapping. |
| 14 | What + opens | Always a Session. |
| 15 | Shortcuts taken from terminals | See Keyboard commands. Zoom moved to Ctrl+Shift+M. |
| 16 | Session name validation | One to 64 printable characters, also enforced by vibed. |

### Work this document now depends on

- The image’s shell configuration must emit OSC 133 prompt marks, and the terminal service must report whether a shell is at its prompt. Until then every close asks.
- vibed’s `rename` must enforce the name rule, and session creation at the limit must return an error the desktop can name.
- The experiment and its README still bind Ctrl+Shift+W and Command+W to Hide window.
- The mockup does not yet show the read-only view with **Take back input**, and it has no window to hide. A browser keeps some chords for itself, such as New Session tab and Close pane, so the mockup’s palette runs those.
