# Documentation Content Design

## Goal

Breadth-first pass across all docs pages so the site feels complete and useful
for early adopters and security-conscious developers evaluating Vibepit.

## Audience

- Developers trying Vibepit with AI coding agents (Claude Code, Cursor, Copilot).
- Security-conscious developers comparing agent sandboxing options.

## Conventions

- Second person ("you"), active voice, concise.
- Name specific agents (Claude Code, Cursor, Copilot) where relevant.
- Each page is self-contained (reader may land from search).
- Prerequisites stated explicitly on every page.

## Scope

End-user focused. No contributor or nested-sandbox content in this pass.

## Nav Structure

```
nav:
  - Home: index.md
  - Tutorials:
      - First Sandbox: tutorials/first-sandbox.md
  - How-To Guides:
      - Manage Allowlist and Monitor: how-to/allowlist-and-monitor.md
      - Configure Network Presets: how-to/configure-presets.md        # new
      - Use with AI Coding Agents: how-to/ai-coding-agents.md        # new
      - Troubleshoot Common Issues: how-to/troubleshooting.md
  - Reference:
      - CLI: reference/cli.md
      - Network Presets: reference/presets.md                         # new
  - Explanations:
      - Architecture: explanations/architecture.md                    # new
      - Security Model: explanations/security-model.md
      - Threat Model: explanations/threat-model.md                    # new
```

## Page Outlines

### 1. First Sandbox (tutorial — deepen existing)

Scope: zero to running sandbox, explains what happens at each step.

- Prerequisites (OS, Docker/Podman, socket access)
- Install vibepit (curl one-liner, move to PATH)
- Move into a project directory
- Run `vibepit` — explain first-run preset selector, auto-detection
- What happens under the hood (network, proxy container, dev container, volume)
- Allow extra domains at startup (`-a`, `-p`)
- Check active sessions (`vibepit sessions`)
- Open the monitor (`vibepit monitor`)
- Reattach to an existing session (exit the shell; `vibepit` in the same
  project directory reattaches automatically if the session is still running)

### 2. Manage Allowlist and Monitor (how-to — deepen existing)

Scope: runtime allowlist management and monitor TUI usage.

- Add HTTP(S) entries (`allow-http`, multiple entries, patterns).
  Wildcard semantics: `*.example.com` matches subdomains only, not the apex.
- Add DNS entries (`allow-dns`)
- `--no-save` flag (skip persisting to config)
- Target a specific session (`--session`)
- Open the monitor TUI — what it shows, how to allow from it
- Session selection when multiple sessions are running

### 3. Configure Network Presets (how-to — new)

Scope: project config file and preset system.

- `.vibepit/network.yaml` file format
- Preset selector (first run and `--reconfigure`)
- Auto-detection from project files (go.mod, package.json, etc.)
- Manual `allow-http` and `allow-dns` entries in config
- Global config (`$XDG_CONFIG_HOME/vibepit/config.yaml`)
- Where each setting comes from (per-field, matching actual code behavior):
  - `allow-http`: presets expanded after explicit HTTP entries
  - `allow-dns`: global + project config (no CLI/preset layer)
  - `block-cidr`: global config only
  - `allow-host-ports`: project config only

### 4. Use with AI Coding Agents (how-to — new)

Scope: advisory agent-specific setup tips, short and practical. Framed as
"tested baseline" presets, not hard requirements (the preset catalog is
data-driven and changes independently).

- Discovery workflow: run agent, check proxy logs/monitor for blocked
  requests, allow what's needed
- Claude Code: common starting presets (anthropic, default), tips
- Cursor: common starting domains/presets
- Copilot: common starting domains/presets
- Encourage iterating via monitor rather than memorizing domain lists

### 5. Troubleshoot Common Issues (how-to — deepen existing)

Scope: expanded failure scenarios with symptoms/cause/fix format.

- Container runtime not available
- allow-http / allow-dns / monitor can't connect
- Image pull or update failures
- DNS resolution failures inside sandbox
- Proxy connection timeouts
- Session won't start (port conflicts, stale networks)
- Config file parse errors
- Permission issues (socket, runtime dir)

### 6. CLI Reference (reference — deepen existing)

Scope: every command, flag, type, default. Source of truth.

- Root command and global flags (`--debug`)
- `run` — all flags (`-L`, `-a`, `-p`, `-r`), project-path argument, behavior notes
- `allow-http` — arguments, `--no-save`, `--session`
- `allow-dns` — arguments, `--no-save`, `--session`
- `sessions` — output format
- `monitor` — `--session`
- `update` — behavior
- `proxy` — internal, brief note
- Environment variables (HTTP_PROXY, etc. set inside container)

### 7. Network Presets (reference — new)

Scope: full preset catalog.

- Table or grouped list: preset name, group, description, all included domains
- Groups: Defaults, Infrastructure, Package Managers
- Note which presets are auto-detected and from which files
- Meta-presets (default includes anthropic + cdn-github + homebrew + openai + vcs-github)

### 8. Architecture (explanation — new)

Scope: how the components fit together.

- Overview: what runs where (host CLI, proxy container, dev container)
- Isolated network (internal bridge, random /24 subnet)
- Proxy container (HTTP proxy, DNS server, control API — three services)
- Dev container (hardened runtime, mounted project, persistent home volume)
- mTLS control API (ephemeral certs, session credentials)
- Data flow: dev container -> proxy -> internet (filtered)
- Session lifecycle (create, attach, detach, cleanup)

### 9. Security Model (explanation — deepen existing)

Scope: what controls exist and why.

- Default-deny posture (no network access unless allowlisted)
- Container hardening (read-only root, dropped caps, no-new-privileges, non-root user, init)
- Network isolation (internal bridge, no direct outbound)
- CIDR blocking (private ranges, localhost, link-local, IPv6)
- DNS filtering (allowlist-only resolution)
- HTTP/HTTPS filtering (domain:port allowlist with wildcard support)
- mTLS control API (Ed25519, TLS 1.3 only, ephemeral CA, CA key discarded)
- Proxy image (distroless, minimal attack surface)
- What this is not (not VM isolation, not absolute containment)

### 10. Threat Model (explanation — new)

Scope: trust boundaries, attacker assumptions, residual risks.

- Trust boundaries (host, proxy container, dev container, network)
- What's in scope (agent code execution, network exfiltration, privilege escalation)
- What's out of scope (kernel exploits, host compromise, supply chain)
- Attacker profile (compromised or misbehaving AI agent)
- Residual risks (container escapes, runtime bugs, DNS rebinding, timing)
- Mitigations and their limits
