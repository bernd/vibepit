# Documentation Content Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Breadth-first pass across all 10 docs pages so the site is complete
and useful for early adopters and security-conscious developers.

**Architecture:** Each task writes or deepens one Markdown page under
`docs/content/`. New pages also require a nav entry in `mkdocs.yml`. Pages are
independent enough to write in any order, but the plan sequences them so
reference pages land first (other pages link to them).

**Tech Stack:** MkDocs + Material, Markdown, admonitions, code blocks.

**Style rules:** Second person ("you"), active voice, concise. Name specific
agents (Claude Code, Cursor, Copilot). Self-contained pages. Prerequisites
stated explicitly. No colloquialisms or product-centric language. See
`docs-style` skill for full guide.

**Design doc:** `docs/plans/2026-02-13-documentation-content-design.md`

**Verification:** After each page, run `cd docs && uv run mkdocs build --strict`
to catch broken links and YAML errors. Preview with `cd docs && uv run mkdocs serve`.

---

### Task 1: Update mkdocs.yml nav with new pages

**Files:**
- Modify: `mkdocs.yml:51-62`

**Step 1: Update the nav section**

Replace the existing `nav:` block with:

```yaml
nav:
  - Home: index.md
  - Tutorials:
      - First Sandbox: tutorials/first-sandbox.md
  - How-To Guides:
      - Manage Allowlist and Monitor: how-to/allowlist-and-monitor.md
      - Configure Network Presets: how-to/configure-presets.md
      - Use with AI Coding Agents: how-to/ai-coding-agents.md
      - Troubleshoot Common Issues: how-to/troubleshooting.md
  - Reference:
      - CLI: reference/cli.md
      - Network Presets: reference/presets.md
  - Explanations:
      - Architecture: explanations/architecture.md
      - Security Model: explanations/security-model.md
      - Threat Model: explanations/threat-model.md
```

**Step 2: Create placeholder files for new pages**

Create empty stubs so `mkdocs build --strict` passes before content is written:

- `docs/content/how-to/configure-presets.md` — `# Configure Network Presets`
- `docs/content/how-to/ai-coding-agents.md` — `# Use with AI Coding Agents`
- `docs/content/reference/presets.md` — `# Network Presets`
- `docs/content/explanations/architecture.md` — `# Architecture`
- `docs/content/explanations/threat-model.md` — `# Threat Model`

**Step 3: Verify build**

Run: `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`
Expected: clean build, no warnings.

**Step 4: Commit**

```bash
git add mkdocs.yml docs/content/how-to/configure-presets.md \
  docs/content/how-to/ai-coding-agents.md docs/content/reference/presets.md \
  docs/content/explanations/architecture.md docs/content/explanations/threat-model.md
git commit -m "Add nav entries and placeholder pages for docs expansion"
```

---

### Task 2: CLI Reference (reference — deepen)

**Files:**
- Modify: `docs/content/reference/cli.md`
- Read: `cmd/root.go`, `cmd/run.go`, `cmd/allow.go`, `cmd/sessions.go`,
  `cmd/monitor.go`, `cmd/update.go`

Write the complete CLI reference. Every command, every flag, types, defaults,
examples. This is the source-of-truth page that other pages link to.

**Sections:**

1. Brief intro: `vibepit` defaults to `run`. Global flag `--debug`.
2. `run` — usage, all flags (`-L`/`--local`, `-a`/`--allow`, `-p`/`--preset`,
   `-r`/`--reconfigure`), `[project-path]` argument. Behavior: finds project
   root via git, refuses home directory, reattaches to existing sessions.
3. `allow-http` — usage, arguments (`<domain:port-pattern>...`), flags
   (`--no-save`, `--session`). Wildcard semantics: `*.example.com` matches
   subdomains only, not the apex domain.
4. `allow-dns` — usage, arguments (`<domain-pattern>...`), flags (`--no-save`,
   `--session`). Same wildcard semantics.
5. `sessions` — usage, output format (session ID, project dir, control port).
6. `monitor` — usage, `--session` flag.
7. `update` — usage, what it pulls.
8. `proxy` — internal command, one-line note.
9. Environment variables set inside the container: `HTTP_PROXY`, `HTTPS_PROXY`,
   `http_proxy`, `https_proxy`.

Use consistent format per command: heading, one-line description, usage code
block, flags table (flag, type, default, description), behavior notes.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Expand CLI reference with all commands and flags"`

---

### Task 3: Network Presets (reference — new)

**Files:**
- Modify: `docs/content/reference/presets.md`
- Read: `proxy/presets.yaml`

Full preset catalog page. Other pages link here instead of duplicating domain
lists.

**Sections:**

1. Brief intro: presets bundle commonly needed domains. Selected during first
   run or via `--reconfigure`. Some presets auto-detect from project files.
2. **Defaults** group: `default` meta-preset (includes anthropic, cdn-github,
   homebrew, openai, vcs-github).
3. **Infrastructure** group: `anthropic`, `openai`, `vcs-github`, `vcs-other`,
   `containers`, `cloud`, `linux-distros`, `devtools`, `monitoring`, `cdn`,
   `schema`, `mcp`. For each: name, description, full domain list.
4. **Package Managers** group: `cdn-github`, `homebrew`, `pkg-node`,
   `pkg-python`, `pkg-ruby`, `pkg-rust`, `pkg-go`, `pkg-jvm`, `pkg-others`.
   For each: name, description, auto-detected files (if any), full domain list.

Use a consistent format per preset: heading, description, auto-detection note
(if applicable), domain list as a fenced code block or bullet list.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Add network presets reference page"`

---

### Task 4: First Sandbox (tutorial — deepen)

**Files:**
- Modify: `docs/content/tutorials/first-sandbox.md`

Rewrite from the existing skeleton. This is the page most new users hit first.

**Sections:**

1. One-line intro: what you'll do (install, launch a sandbox, see it working).
2. Prerequisites: Linux or macOS, Docker or Podman running, socket access.
3. Install vibepit (curl one-liner + move to PATH).
4. Move into a project directory. Explain Vibepit binds this directory into the
   sandbox. Note: refuses to run in home directory.
5. Run `vibepit`. Walk through first-run experience: preset selector appears,
   auto-detects language presets from project files, `default` preset
   pre-selected. Explain what the selector is doing.
6. What happens under the hood: creates isolated Docker network, starts proxy
   container (DNS + HTTP filtering), starts dev container (read-only root,
   dropped caps, your project mounted in, persistent home volume). Brief — link
   to Architecture page for details.
7. Allow extra domains at startup: `-a example.com:443`, `-p github`. Link to
   CLI Reference for full flag list.
8. Inside the sandbox: your project is mounted, `HTTP_PROXY`/`HTTPS_PROXY` are
   set, only allowlisted domains are reachable.
9. Check active sessions: `vibepit sessions`.
10. Open the monitor: `vibepit monitor`. One sentence on what it shows — link to
    Manage Allowlist and Monitor for details.
11. Reattach to a session: exit the shell, run `vibepit` again in the same
    project directory. If the session is still running, you reattach
    automatically.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Expand first sandbox tutorial with full walkthrough"`

---

### Task 5: Manage Allowlist and Monitor (how-to — deepen)

**Files:**
- Modify: `docs/content/how-to/allowlist-and-monitor.md`

Deepen the existing skeleton with practical detail.

**Sections:**

1. Brief intro: how to update network permissions and inspect traffic for a
   running session.
2. Add HTTP(S) entries: `vibepit allow-http api.example.com:443`. Multiple
   entries in one command. Wildcard semantics: `*.example.com` matches
   subdomains only, not the apex domain. Port patterns support digits and `*`.
3. Add DNS entries: `vibepit allow-dns internal.example.com`. Same wildcard
   semantics.
4. Skip saving to config: `--no-save` flag. By default entries are persisted to
   `.vibepit/network.yaml`.
5. Target a specific session: `--session <session-id>`. When to use (multiple
   sessions running). Get session IDs from `vibepit sessions`.
6. Open the monitor: `vibepit monitor`. What it shows (live proxy logs, blocked
   and allowed requests). How to allow domains directly from the monitor.
7. Session selection: when multiple sessions are running and `--session` is not
   provided, an interactive selector appears.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Expand allowlist and monitor how-to guide"`

---

### Task 6: Configure Network Presets (how-to — new)

**Files:**
- Modify: `docs/content/how-to/configure-presets.md`
- Read: `config/config.go`, `config/setup.go`

**Sections:**

1. Brief intro: how to configure which domains your sandbox can reach, using
   presets and manual entries.
2. The config file: `.vibepit/network.yaml` in your project root. Show example:

   ```yaml
   presets:
     - default
     - pkg-go

   allow-http:
     - api.openai.com:443

   allow-dns:
     - internal.corp.example.com
   ```

3. First-run preset selector: runs automatically on first `vibepit` in a
   project. Auto-detects language presets from project files (e.g., `go.mod` →
   `pkg-go`). `default` preset is pre-selected.
4. Reconfigure: `vibepit --reconfigure` or `vibepit -r`. Reruns the selector,
   preserves existing `allow-http` and `allow-dns` entries.
5. Manual entries: add `allow-http` and `allow-dns` entries directly to the
   config file. Same wildcard syntax as CLI commands.
6. `allow-host-ports`: list of port numbers on `host.vibepit` reachable from the
   container. Project config only.
7. Global config: `$XDG_CONFIG_HOME/vibepit/config.yaml`. Same keys.
8. Where each setting comes from:
   - `presets`: project config, expanded after loading
   - `allow-http`: presets expanded after explicit HTTP entries
   - `allow-dns`: global + project config (no CLI/preset layer)
   - `block-cidr`: global config only
   - `allow-host-ports`: project config only
9. Link to Network Presets reference for the full preset catalog.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Add configure network presets how-to guide"`

---

### Task 7: Use with AI Coding Agents (how-to — new)

**Files:**
- Modify: `docs/content/how-to/ai-coding-agents.md`

Advisory page — "tested baseline" presets, not hard requirements. Lead with the
discovery workflow.

**Sections:**

1. Brief intro: Vibepit works with any AI coding agent that runs in a terminal.
   This page covers common starting presets and a workflow for discovering what
   your agent needs.
2. Discovery workflow: start with the `default` preset, run your agent, open the
   monitor (`vibepit monitor`), look for blocked requests, allow what's needed
   via the monitor or `allow-http`/`allow-dns`. Iterate.
3. Claude Code: common starting presets (`default` covers it — includes
   `anthropic`). If using MCP servers, may need additional domains. Mention
   `mcp` preset.
4. Cursor: common starting domains. Note that Cursor needs access to its own
   backend services — check the monitor for blocked requests and allow as
   needed.
5. GitHub Copilot: common starting domains. Similar approach — start with
   `default`, add `vcs-github` if not already included, check monitor.
6. General advice: every agent evolves, so domain lists change. The monitor is
   the best way to discover what's needed. Link to Manage Allowlist and Monitor.

Keep each agent section short (3-5 sentences + a code example).

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Add AI coding agents how-to guide"`

---

### Task 8: Troubleshoot Common Issues (how-to — deepen)

**Files:**
- Modify: `docs/content/how-to/troubleshooting.md`

Expand with more failure scenarios. Use consistent format: symptom, cause, fix.

**Sections:**

1. Container runtime not available — symptoms, check `docker info`/`podman info`,
   socket permissions.
2. `allow-http`/`allow-dns`/`monitor` can't connect — no running session, wrong
   session targeted, check `vibepit sessions`.
3. Image pull or update failures — network filtering blocks registry, allow
   required registries or run outside sandbox.
4. DNS resolution failures inside sandbox — domain not in allowlist, add via
   `allow-dns` or monitor. Check if the domain is in a preset you haven't
   enabled.
5. HTTP requests failing inside sandbox — domain:port not in allowlist, add via
   `allow-http`. Check for wildcard mismatch (apex vs subdomain).
6. Session won't start — port conflicts, stale Docker networks. Try
   `docker network prune` (after confirming no other containers need them).
7. Config file parse errors — YAML syntax, check indentation, validate with
   `vibepit --reconfigure`.
8. Permission issues — runtime socket access, XDG_RUNTIME_DIR permissions.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Expand troubleshooting guide with more scenarios"`

---

### Task 9: Architecture (explanation — new)

**Files:**
- Modify: `docs/content/explanations/architecture.md`
- Read: `cmd/session.go`, `proxy/server.go`, `container/client.go`

**Sections:**

1. Overview: Vibepit runs three components — a host CLI, a proxy container, and
   a dev container — connected by an isolated Docker network.
2. Host CLI: orchestrates everything. Creates the network, starts containers,
   manages session credentials, provides `allow-*` and `monitor` commands.
3. Isolated network: internal Docker bridge network with a random `10.x.x.0/24`
   subnet. `Internal: true` means no direct outbound access — all traffic must
   go through the proxy.
4. Proxy container: runs on distroless base image. Three services:
   - HTTP proxy (dynamic port) — filters HTTP/HTTPS via allowlist
   - DNS server (port 53) — filters DNS queries via allowlist
   - Control API (dynamic port) — mTLS-secured, used by CLI commands
   All three share the same allowlist, updated atomically at runtime.
5. Dev container: your workspace. Read-only root filesystem, dropped
   capabilities, `no-new-privileges`, non-root `code` user, init process.
   Project directory mounted in. Persistent `vibepit-home` volume for tools
   and config across sessions.
6. Data flow: dev container → proxy → internet. DNS queries go to proxy port 53.
   HTTP/HTTPS goes through `HTTP_PROXY`/`HTTPS_PROXY`. Proxy checks allowlist
   and CIDR blocklist before forwarding.
7. Session lifecycle: `vibepit` creates network + containers, generates
   ephemeral mTLS certs, stores credentials in `$XDG_RUNTIME_DIR/vibepit/`.
   Exiting the shell stops the dev container. Rerunning `vibepit` in the same
   project reattaches if the session is still running. Cleanup removes network
   and credentials.
8. Control API: mTLS with ephemeral Ed25519 certificates. CA key discarded
   after signing. TLS 1.3 only. Used by `allow-http`, `allow-dns`, `monitor`.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Add architecture explanation page"`

---

### Task 10: Security Model (explanation — deepen)

**Files:**
- Modify: `docs/content/explanations/security-model.md`
- Read: `proxy/cidr.go`, `proxy/allowlist.go`, `proxy/mtls.go`

Rewrite from the existing skeleton. Explain what each control does and why.

**Sections:**

1. Brief intro: Vibepit applies defense-in-depth controls to reduce risk when
   running AI coding agents.
2. Default-deny posture: no network access unless explicitly allowlisted. No DNS
   resolution, no HTTP/HTTPS, no direct IP access.
3. Container hardening:
   - Read-only root filesystem — prevents persistent modifications
   - All Linux capabilities dropped — minimal privilege
   - `no-new-privileges` — prevents privilege escalation via setuid/setgid
   - Non-root `code` user — limits impact of container-level compromise
   - Init process — proper signal handling, zombie reaping
4. Network isolation: internal Docker bridge, no direct outbound. All traffic
   routes through the proxy.
5. CIDR blocking: private networks (`10.0.0.0/8`, `172.16.0.0/12`,
   `192.168.0.0/16`), localhost (`127.0.0.0/8`), link-local
   (`169.254.0.0/16`), IPv6 equivalents (`fc00::/7`, `fe80::/10`, `::1/128`).
   Blocked regardless of allowlist.
6. DNS filtering: only allowlisted domains resolve. Wildcard support
   (`*.example.com` matches subdomains, not apex).
7. HTTP/HTTPS filtering: domain:port allowlist. Same wildcard semantics. Port
   patterns support digits and `*` globs.
8. mTLS control API: ephemeral Ed25519 certificates per session, CA key
   discarded after signing, TLS 1.3 only, server cert SAN restricted to
   `127.0.0.1`.
9. Proxy image: `gcr.io/distroless/base-debian13` — no shell, no package
   manager, minimal attack surface.
10. What this is not: not VM isolation. Container escapes, kernel
    vulnerabilities, and runtime bugs can weaken isolation. Treat as defense in
    depth, not absolute containment.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Expand security model explanation"`

---

### Task 11: Threat Model (explanation — new)

**Files:**
- Modify: `docs/content/explanations/threat-model.md`

**Sections:**

1. Brief intro: what Vibepit defends against and where its boundaries are.
2. Primary attacker profile: a compromised or misbehaving AI coding agent
   running inside the dev container. The agent has shell access and can execute
   arbitrary code within the container.
3. Trust boundaries:
   - Host system (fully trusted)
   - Proxy container (trusted, minimal attack surface)
   - Dev container (untrusted — this is where the agent runs)
   - Network boundary (enforced by proxy)
4. What Vibepit defends against (in scope):
   - Network exfiltration — blocked by default-deny allowlist
   - Data exfiltration via DNS — blocked by DNS filtering
   - Lateral movement to host/other containers — blocked by CIDR blocklist and
     internal network
   - Privilege escalation inside container — mitigated by dropped caps and
     no-new-privileges
   - Persistent filesystem compromise — mitigated by read-only root
5. What Vibepit does not defend against (out of scope):
   - Container escape via kernel vulnerabilities
   - Host compromise (if the container runtime itself is compromised)
   - Supply chain attacks in allowlisted dependencies
   - Side-channel attacks
   - Social engineering via agent output (Vibepit filters network, not terminal
     output)
6. Residual risks:
   - Container escapes exist — Vibepit reduces but cannot eliminate this risk
   - DNS rebinding attacks against allowlisted domains
   - Time-of-check-to-time-of-use gaps in allowlist updates
   - Covert channels via allowed network connections
7. Mitigations and their limits: link to Security Model for the specific
   controls. Note that Vibepit is one layer — users should also review agent
   output, limit allowlisted domains to what's needed, and keep container
   runtimes updated.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Add threat model explanation page"`

---

### Task 12: Update landing page links

**Files:**
- Modify: `docs/content/index.md`

Update the "Start Here" cards section to include the new pages. Add cards for
Architecture and Configure Network Presets. Keep the existing 4 cards, add 2
more for a 6-card grid (or keep 4 and swap if the grid looks better with 4).

Check that all existing links still resolve correctly.

**Verify:** `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

**Commit:** `git commit -m "Update landing page links for new docs pages"`

---

### Task 13: Final build verification

Run: `cd /home/bernd/Code/vibepit/docs && uv run mkdocs build --strict`

Verify:
- Clean build with no warnings
- All nav links resolve
- No broken cross-page links
