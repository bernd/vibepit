---
description: Complete reference for vibepit commands, flags, and arguments including run, up, down, connect, exec, status, allow-http, allow-dns, deny-http, deny-dns, monitor, and update.
---

# CLI Reference

`vibepit` is a single binary that manages sandbox sessions, network filtering,
and runtime administration. When you run `vibepit` without a subcommand, it
defaults to the [`run`](#run) command.

```
vibepit [global-flags] [command] [command-flags] [arguments]
```

## Global flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--debug` | bool | `false` | Enable debug output |

---

## `run`

Start a new sandbox session or attach to an existing one.

```
vibepit run [flags] [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-L`, `--local` | bool | `false` | Use the local `vibepit:latest` image instead of the published one. Required when you [build a custom image](../how-to/troubleshooting.md#sandbox-image-not-found) for an unsupported UID/GID combination. |
| `-a`, `--allow` | string (repeatable) | | Additional `domain:port` entries to allow through the proxy (e.g. `api.example.com:443`) |
| `-p`, `--preset` | string (repeatable) | | Additional network presets to activate |
| `-m`, `--memory` | string | | Memory limit for the sandbox container (e.g. `16g`, `512m`; `0` for none). Overrides the `memory` config setting. |
| `-r`, `--reconfigure` | bool | `false` | Re-run the network preset selector |
| `--prompt` | bool | [`prompt`](../how-to/configure-presets.md#prompt-by-default) in the global config, else `false` | Show an allow/deny prompt over the session when the proxy blocks a connection. See [Blocked connection prompt](#blocked-connection-prompt). |

### Behavior

- If `project-path` is omitted, `vibepit` uses the current working directory.
- If the directory is inside a Git repository, `vibepit` resolves to the
  repository root and uses that as the project directory.
- `vibepit` refuses to run if the resolved project directory is your home
  directory.
- If a session is already running for the same project directory, `vibepit`
  attaches to it instead of starting a new one. The flags above only apply when
  a session starts, so `vibepit` warns that it ignores any that are set.
- On first run in a project, `vibepit` launches an interactive setup flow to
  select network presets. Pass `--reconfigure` to re-run this selector later.
- `vibepit` merges entries passed with `--allow` and `--preset` with the
  entries saved in the project configuration file.
- With `--prompt`, blocked connections open an allow/deny prompt over the
  session. See [Blocked connection prompt](#blocked-connection-prompt).

### Blocked connection prompt

With `--prompt`, `run` and `connect` ask you right away whether to allow a connection
the proxy would block. The prompt appears over the session, in the same
terminal window, in any terminal emulator. Once you decide or dismiss it,
the agent's screen comes back as it was, including any output the agent
wrote in the meantime.

To get the prompt in every session without the flag, see
[Prompt by default](../how-to/configure-presets.md#prompt-by-default).

While a prompt is attached, the proxy holds the agent's HTTP and HTTPS
request instead of failing it. The prompt shows `held` and "the agent is
waiting" with the seconds left. If you allow the target in time, the request
goes through and the agent never sees a failure.
[`hold-timeout`](../how-to/configure-presets.md#hold-blocked-requests) sets how
long the proxy waits (10 seconds by default).

| Key | Action |
|-----|--------|
| `a` | Allow for the rest of the session |
| `A` | Allow and save to the project configuration |
| `N` | Enforce a deny immediately and save the exact target to the project configuration (`deny+save`). |
| `n` | Reject the request and suppress prompts for this session (`reject`). The held request fails at once, and other clients stop asking about this target for the rest of the session. |
| `Esc`, `q` | Dismiss without deciding. The request keeps waiting until the time runs out, and other clients still ask. |

Behavior details:

- When several targets are held at once, the prompt lists the others under
  "also waiting (N)". Decide the first one and it disappears, then the next
  one moves up.
- When the time runs out, the agent's request fails, but the prompt stays
  open as a regular blocked prompt. Allowing it then lets the agent's next
  attempt through.
- The proxy holds only HTTP and HTTPS requests. It blocks DNS lookups right
  away.
- When no prompt or monitor is attached, nothing is held and blocked
  requests fail right away. The same happens when `hold-timeout` is `0`, and
  when the last prompt or monitor disconnects, which fails the waiting
  requests at once.
- The proxy holds at most 32 targets and 64 waiting requests, and blocks any
  requests beyond that right away.
- The agent keeps running while the prompt shows. Its output appears when
  the prompt closes.
- A target you dismiss asks again on the agent's next attempt. A denied
  target doesn't ask again for the rest of the session.
- Only targets missing from the allowlist prompt and get held. A target that
  resolves to a blocked IP range, or that the proxy couldn't resolve for that
  check, stays blocked whatever you allow, so it doesn't prompt.
- Prompts for different targets open one after another, never on top of
  each other.
- When several clients are attached to one session, each shows the prompt.
  As soon as one of them allows or denies, the others close their prompts.
- The proxy keeps targets suppressed with lowercase `n` in memory and forgets
  them when the session stops. To allow a suppressed target later, use
  [`allow-http`](#allow-http), [`allow-dns`](#allow-dns), or
  [`monitor`](#monitor).
- Uppercase `N` installs an enforced deny that overrides allows and presets.
  An HTTP deny keeps the target's port, and a DNS deny is a bare domain. If
  saving fails, the deny stays active in the proxy and the error says it was
  not saved. The target stays on top, and `N` retries the save. `Esc` skips
  only that target so you can still decide the others, and `q` dismisses them
  all. Explicitly denied targets never prompt or enter the hold queue. To
  remove a saved deny, edit the config and restart the session.
- Blocked IPv6 address targets don't open the prompt. Allow and deny rules
  can't contain IPv6 literals, so there is nothing to decide, and the proxy
  doesn't hold these requests. Use `block-cidr` to block address ranges.
- Before the prompt appears, Vibepit asks the terminal for a status report
  to find the exact point where your typing ends and the prompt's input
  begins. If the terminal doesn't answer, Vibepit skips the prompt and shows
  it on the next hold or block of that target. After two misses, Vibepit
  switches input to the prompt after a short pause in typing instead.
- When Vibepit can't show a prompt, or your terminal lacks a feature prompts
  need, Vibepit prints a warning once the session ends.
- When the agent uses the whole screen (for example an editor), or the
  terminal is resized while the prompt shows, Vibepit redraws the screen
  from its own copy. Lines that scrolled off in the meantime are then
  missing from the terminal's scrollback, and hyperlinks and images are not
  restored.

### Examples

```bash
# Start a session in the current directory
vibepit

# Start a session for a specific project
vibepit run ~/projects/my-app

# Use a locally built image
vibepit run -L

# Allow access to an additional domain
vibepit run -a api.example.com:443

# Allow multiple domains and enable a preset
vibepit run -a api.example.com:443 -a cdn.example.com:443 -p vcs-github

# Re-run the network preset selector
vibepit run -r

# Prompt to allow connections the proxy blocks
vibepit run --prompt
```

---

## `up`

Start a sandbox session in daemon mode. The sandbox and proxy containers run in
the background with an SSH server, and `vibepit` returns immediately after the
session is ready.

```
vibepit up [flags] [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-L`, `--local` | bool | `false` | Use the local `vibepit:latest` image instead of the published one. |
| `-a`, `--allow` | string (repeatable) | | Additional `domain:port` entries to allow through the proxy (e.g. `api.example.com:443`) |
| `-p`, `--preset` | string (repeatable) | | Additional network presets to activate |
| `-m`, `--memory` | string | | Memory limit for the sandbox container (e.g. `16g`, `512m`; `0` for none). Overrides the `memory` config setting. |
| `-r`, `--reconfigure` | bool | `false` | Re-run the network preset selector |

### Behavior

- Creates an isolated network, proxy container, and sandbox container, the same
  as [`run`](#run).
- Generates ephemeral SSH keypairs (one for the client, one for the host)
  and stores them in `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`.
- The sandbox container runs an SSH server on internal port 2222. The proxy
  container forwards it and publishes it to `127.0.0.1` on a random host port.
- Waits for the SSH daemon to accept connections before returning.
- If a session is already running for the same project directory, prints a
  message and exits without starting a new one. It warns that it ignores any
  flags you set.
- If it finds orphaned containers from a previous session, it exits with an
  error that asks you to run `vibepit down` first.

### Examples

```bash
# Start a daemon-mode session in the current directory
vibepit up

# Start with a network preset
vibepit up -p vcs-github

# Start for a specific project
vibepit up ~/projects/my-app
```

---

## `down`

Stop and remove sandbox and proxy containers for a session.

```
vibepit down [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Behavior

- Finds the running session for the project directory.
- Stops and removes all containers belonging to the session (sandbox and proxy).
- Removes the session network.
- Deletes session credentials (mTLS and SSH keys) from
  `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`.
- If it cannot remove some containers, it keeps the credentials so you can
  retry.
- Also detects orphaned containers (e.g., proxy still running after sandbox
  crashed) and cleans them up.

### Examples

```bash
# Stop the session for the current directory
vibepit down

# Stop the session for a specific project
vibepit down ~/projects/my-app
```

---

## `connect`

Aliases: `c`

Connect to a running sandbox.

```
vibepit connect
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--prompt` | bool | [`prompt`](../how-to/configure-presets.md#prompt-by-default) in the global config, else `false` | Show an allow/deny prompt over the session when the proxy blocks a connection. See [Blocked connection prompt](#blocked-connection-prompt). |
| `-L`, `--forward` | string (repeatable) | | Forward a host port to a port inside the sandbox while connected: `[[BIND:]LOCAL:[HOST:]]REMOTE`. `BIND` defaults to `127.0.0.1`; an empty or `*` `BIND` listens on all interfaces. `HOST` defaults to `localhost` and must be a loopback address in the sandbox. Put IPv6 addresses in brackets, e.g. `[::1]`. |

### Behavior

- Resolves the project root from the current working directory and finds the
  running session.
- Loads the SSH client key from the session credentials directory.
- Connects to `127.0.0.1` on the published SSH port with public key
  authentication.
- With `--forward`, listens on each local port before the shell starts and
  tunnels every connection over the SSH connection to the sandbox, like
  `ssh -L`. Forwards stop when `connect` exits. The sandbox SSH server refuses
  forwards to anything other than its own loopback. When `connect` starts a
  new shell session, a banner above the first prompt lists the forwards. It
  stays in that session's scrollback. Reattaching to an existing session
  shows no banner.
- Requests a PTY, starts a shell, and forwards terminal resize events
  (`SIGWINCH`).
- When detached sessions exist inside the sandbox, the SSH server shows a
  session selector. You can reattach to a previous session or start a new one.
- With `--prompt`, blocked connections open an allow/deny prompt over the
  session, as with `run --prompt`. See
  [Blocked connection prompt](#blocked-connection-prompt).

### Examples

```bash
# Open an interactive shell
vibepit connect

# Prompt to allow connections the proxy blocks
vibepit connect --prompt

# Reach a dev server on sandbox port 3000 at http://127.0.0.1:3000
vibepit connect -L 3000

# Host port 8080 to sandbox port 3000, plus a second forward
vibepit connect -L 8080:3000 -L 5432:localhost:5432
```

---

## `exec`

Execute a command in the sandbox.

```
vibepit exec <command...>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `command...` | The remote command to execute. |

### Behavior

- Resolves the project root from the current working directory and finds the
  running session.
- Loads the SSH client key from the session credentials directory.
- Connects to `127.0.0.1` on the published SSH port with public key
  authentication.
- Runs the command in the sandbox and returns its exit code. It forwards
  stdin, stdout, and stderr.

### Examples

```bash
# Run a single command
vibepit exec ls -la

# Run a command that reads a file
vibepit exec cat /etc/os-release
```

---

## `status`

Show session status.

```
vibepit status [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-v`, `--verbose` | bool | `false` | Enable verbose output |

### Output

When run inside a project directory (or given a project path), displays the
session status for that project. When no session is found for the current
project, or when run outside a project directory, displays status for all
running sessions on the machine.

Example output:

```
  Session   cq1abc2def3gh4ij
  Project   /home/user/my-project
  Sandbox   running: vibepit-sandbox-cq1abc2def3gh4ij (up 2m30s)
   Memory   8GiB
    Proxy   running: vibepit-proxy-cq1abc2def3gh4ij (up 2m31s)
      API   127.0.0.1:41923
      SSH   127.0.0.1:52847
```

If no sessions are running at all, prints `No active session for <path>` (when
in a project) or `No active sessions` (when outside a project).

---

## `allow-http`

Add HTTP(S) allowlist entries for a running session. By default, the command
also saves the entries to the project configuration file so they apply on
future runs.

```
vibepit allow-http [flags] <domain:port-pattern>...
```

### Arguments

| Argument | Description |
|----------|-------------|
| `domain:port-pattern` | One or more domain-and-port patterns to allow. Required. The port is required too. Use `example.com:443` for HTTPS, `example.com:80` for HTTP, or `example.com:*` for any port. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-save` | bool | `false` | Don't save the entries to the project config |
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Wildcard semantics

`*` matches exactly one DNS label. `**` matches one or more labels. Both can
appear in any position, but a pattern can contain at most one `**`.

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com` | `example.com`, `a.b.example.com` |
| `**.example.com:443` | `api.example.com`, `a.b.example.com` | `example.com` |
| `bedrock.*.amazonaws.com:443` | `bedrock.us-east-1.amazonaws.com` | `bedrock.a.b.amazonaws.com` |

Ports must be an exact number or `*` for any port. Ports compare as numbers,
so `example.com:0443` is the same entry as `example.com:443`.

### Examples

```bash
# Allow a single domain
vibepit allow-http api.example.com:443

# Allow all subdomains (one level) of a domain
vibepit allow-http '*.example.com:443'

# Allow multiple entries without saving to config
vibepit allow-http --no-save api.example.com:443 cdn.example.com:443

# Target a specific session
vibepit allow-http --session my-session-id api.example.com:443
```

---

## `allow-dns`

Add DNS allowlist entries for a running session. By default, the command also
saves the entries to the project configuration file so they apply on future
runs.

```
vibepit allow-dns [flags] <domain-pattern>...
```

### Arguments

| Argument | Description |
|----------|-------------|
| `domain-pattern` | One or more domain patterns to allow DNS resolution for. Required. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-save` | bool | `false` | Don't save the entries to the project config |
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Wildcard semantics

`*` matches exactly one DNS label. `**` matches one or more labels. Both can
appear in any position, but a pattern can contain at most one `**`.

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com` | `api.example.com` | `example.com`, `a.b.example.com` |
| `**.example.com` | `api.example.com`, `a.b.example.com` | `example.com` |

### Examples

```bash
# Allow DNS resolution for a domain
vibepit allow-dns example.com

# Allow DNS resolution for all subdomains
vibepit allow-dns '*.example.com'

# Allow without saving to config
vibepit allow-dns --no-save example.com
```

---

## `deny-http`

Enforce HTTP(S) deny rules in a running proxy. Matching denies override every
allow, including presets, CLI allows, and `host.vibepit` exceptions.

```
vibepit deny-http [flags] <domain:port-pattern>...
```

Entries need a port and use the same [wildcard semantics](#wildcard-semantics)
as `allow-http`. You can pass several entries. With no entries, the command
shows help. The command validates entries before it looks for a session.

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-save` | bool | `false` | Enforce rules until the proxy stops, without saving them |
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Behavior

Rules apply immediately, and matching held requests fail, including every
target a wildcard matches. A rule also blocks an allowed host that is an alias
(CNAME) of a denied one. While HTTP deny rules exist, the proxy looks up the
alias chain of each allowed host. It refuses the request if a name in the
chain is denied for that port, or if the lookup fails.

By default, the command also saves the rules to the selected session's
`.vibepit/network.yaml`. If that file is missing, the command creates it with
the deny rules and the presets that first-run setup would pre-select, which
are `default` plus the presets detected in the project. The command reports
each applied rule and where it saved it.

If the proxy fails to apply a rule, the command returns an error and saves
nothing. If saving fails, the error says the deny is active in the proxy but
not saved. An older proxy without support for enforced denies returns an
error.

`--no-save` still enforces the deny, unlike lowercase `n` in the prompt.
Adding an allow cannot override it. `allow-http`, `allow-dns`, and the allow
keys refuse an exact target that a deny rule matches. They refuse the whole
request, the error names every denied entry, and nothing is allowed or saved.
A wildcard allow is accepted, but the deny still wins for the targets it
covers. To remove a saved deny, edit the config file and restart the session.
Tunnels that are already open stay open.

```bash
vibepit deny-http telemetry.example.com:443 '*.tracking.example.com:*'
vibepit deny-http --session my-session-id --no-save telemetry.example.com:443
```

---

## `deny-dns`

Enforce DNS deny rules in a running proxy and save them by default.

```
vibepit deny-dns [flags] <domain-pattern>...
```

Entries are domain patterns without ports and use the same wildcard syntax as
[`allow-dns`](#allow-dns). Multiple entries, validation, session selection,
saving, errors, and the `--session` and `--no-save` flags work as for
[`deny-http`](#deny-http). With no entries, the command shows help.

A DNS deny applies to the sandbox DNS service, not to the HTTP proxy's own
upstream lookups. An HTTP deny does not block DNS. To block a host on both
paths, add rules to both lists. See [Deny rules](../how-to/configure-presets.md#deny-rules).

```bash
vibepit deny-dns telemetry.example.com '**.tracking.example.com'
vibepit deny-dns --session my-session-id --no-save telemetry.example.com
```

---

## `monitor`

Aliases: `m`, `tv`

Open a terminal UI that shows proxy logs for a running session and lets you
allow or deny hosts.

```
vibepit monitor [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Behavior

- If you omit `--session` and several sessions are running, `vibepit` shows a
  session selector.
- If only one session is running, `vibepit` connects to it.
- The logs and held requests update live from the proxy's event stream.
  Host statistics follow the stream too, and a periodic poll adds the
  allowlist and deny status.
- While the monitor is attached, the proxy holds requests to targets that
  are not in the allowlist, as with [`--prompt`](#blocked-connection-prompt).
  Held requests are yellow on both tabs. The logs tab shows "held · waiting
  for a decision" with the seconds left, and once the hold ends it shows how
  it ended: allowed, rejected, denied, timed out, request gone, or nobody
  watching.
  The hosts tab pins held hosts at the top with "held Ns", and the summary
  line counts them as "● N held".
- On a held request, `a` or `A` allows it and `n` (`reject`) rejects it and
  suppresses prompts for this session. The host then shows "rejected", and
  later requests to it are logged as "rejected this session" until it is
  allowed.
- On either tab, `N` (`deny+save`) enforces and saves a deny for the selected
  HTTP or DNS target, including allowed hosts and expired holds. The host
  status shows "denied", and the summary line counts it as "● N denied". Log
  lines for requests a deny rule blocked are marked `-` instead of `x`, and
  older log lines stay as they were. The allow keys cannot override the deny.
  If saving fails, the error says the deny is active in the proxy, and `N`
  changes to `retry save`. On a target that is already denied, `N` is not
  available.

---

## `update`

Update the vibepit binary and pull the latest container images.

```
vibepit update [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--bin` | bool | `false` | Update the binary only (skip image pull) |
| `--images` | bool | `false` | Update container images only (skip binary update) |
| `--check` | bool | `false` | Check for available updates without installing |
| `--list` | bool | `false` | List available releases |
| `--use` | string | | Install a specific version (implies `--bin`) |
| `--pre` | bool | `false` | Use the prerelease channel |
| `-y`, `--yes` | bool | `false` | Skip the confirmation prompt |

### Behavior

By default, `vibepit update` updates both the binary and the container images.
Use `--bin` or `--images` to update only one. The two flags are mutually
exclusive.

**Binary update:**

- Checks the release channel for a newer version and downloads it.
- Verifies the download with SHA-256 checksums and cosign signature bundles
  (when available).
- Replaces the running binary in place.
- If you installed vibepit with a package manager (Homebrew, Snap, Nix, and so
  on), `update` skips the binary and tells you to use the package manager.
- Use `--use <version>` to install a specific version regardless of what the
  channel reports as latest.
- Use `--pre` to check the prerelease channel instead of the stable channel.

**Image update:**

- Pulls the latest sandbox image for your UID/GID combination (e.g.,
  `ghcr.io/bernd/vibepit:r1-uid-1000-gid-1000`).
- Pulls the latest proxy base image.

### Examples

```bash
# Update everything (binary + images)
vibepit update

# Check if an update is available
vibepit update --check

# List available releases
vibepit update --list

# Update binary only, skip confirmation
vibepit update --bin -y

# Update images only
vibepit update --images

# Install a specific version
vibepit update --use 0.2.0

# Check the prerelease channel
vibepit update --check --pre
```

