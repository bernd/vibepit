---
name: creating-changelog
description: Use when writing, generating, or updating a vibepit changelog / release notes (e.g. "changelog for v0.3.0", "changelog since v0.2.0", "add a changelog entry"). Covers the docs/changelogs/*.yml format consumed by the release pipeline.
---

# Creating a Vibepit Changelog

## When to Write It

The changelog is normally assembled **once, shortly before cutting a release**, in a single pass
over the whole commit range since the previous tag (`git log --oneline <prev-tag>..HEAD`). That way
nothing is forgotten and entries read consistently. `git log` is the record of what changed, so
there's no need to add an entry for every PR as you go.

## Overview

Vibepit changelogs are **per-version YAML files** at `docs/changelogs/{version}.yml`.
They are NOT a top-level `CHANGELOG.md`. At release time, `.github/scripts/generate-release-metadata.py`
parses the file matching the git tag and renders it into `docs/content/releases/{version}.json`
and the release notes, turning `pr`/`issue` numbers into GitHub links.

**Do not** create `CHANGELOG.md`, edit the rendered `.json` files by hand, or invent the schema —
write one YAML file in `docs/changelogs/`.

## File Format

Filename is the **bare version, no `v` prefix**: `0.3.0.yml` (matches the release tag `v0.3.0`).

```yaml
version: "0.3.0"

added:
  - msg: "`allow-cidr` config option to explicitly allow local IP ranges"
    pr: "10"
  - msg: JetBrains AI service network preset

fixed:
  - msg: Preserve formatting in `allow-http`/`allow-dns` appends
    pr: "8"
  - msg: "`install -m` parameter in the Dockerfile"
```

- `version` is a quoted string.
- Categories, rendered in this fixed order: **`added`, `changed`, `fixed`, `deprecated`, `removed`, `security`**. Omit any category with no entries.
- Each entry is a map with a required `msg`. Optional `pr` and `issue` are quoted strings; the script renders them as `(#N)` GitHub links — do **not** put links in `msg` yourself.
- Quote any `msg` that starts with a backtick (otherwise YAML parses it oddly): `msg: "`extra-hosts` config option"`.
- See `docs/changelogs/TEMPLATE.yml` and existing files (`0.2.0.yml`) for the canonical shape.

## Workflow

1. Find the range: `git log --oneline <prev-tag>..HEAD` (e.g. `v0.2.0..HEAD`).
2. Pick the new version number and name the file `{bare_version}.yml`.
3. Classify each user-facing commit into a category and write a `msg` **reworded for users** (see below).
4. Attach `pr:`/`issue:` when the commit references one (`(#11)` in the subject, or check `git log`).
5. Within each category, order entries however reads best — existing files trend toward **ascending PR number**.

## Write for Users, Not Developers

The commit subject is written for developers; the changelog `msg` is read by users who run
`vibepit` but don't know its internals. **Reword anything technical** so it describes the
effect on the user, not the code change. Don't just paste the commit subject.

Ask: "What can the user now do / no longer worry about?" Name the user-facing thing (a command,
flag, config key, behavior) and drop internal terms (function names, packages, refactor jargon,
test-infra, "gate", "race", file paths) unless they're genuinely user-visible.

| Commit subject (too technical) | Changelog `msg` (user-facing) |
|---|---|
| `Distinguish forced detaches from clean shell exits` | Show a clear message when a session is forcibly detached instead of exiting |
| `Gate test teardown on full session exit to fix state-file cleanup race` | Fix stale session state left behind after disconnecting |
| `Preserve formatting in allow-http/allow-dns appends` | Keep your config file's formatting intact when adding allowlist entries |
| `Force anonymous auth for cosign image signature verification` | Fix image signature verification failing for users not logged in to a registry |
| `Add allow-cidr config option` | Add `allow-cidr` to explicitly allow access to local IP ranges |

Keep it concise (one line), present-tense, and concrete. If you genuinely can't tell what a
commit means for users, read the PR/diff rather than guessing — or leave it out if it's internal-only.

## What to Include / Omit

Changelogs describe **user-facing changes**, not the commit history. Omit:

- Routine dependency bumps and tooling version updates ("Update dependencies", "Update cosign to X.Y.Z").
- Internal-only commits: `go fmt`, lint fixes, test-infra/teardown tweaks, refactors with no behavior change.
- Release-plumbing commits for the *previous* version (e.g. "generate release metadata for v0.2.0", changelog-formatting fixups) — those belong to the already-shipped release.

Include: new commands/flags/config options, network presets, behavior changes users would notice, bug fixes, security fixes, removals/deprecations, and notable new user-facing docs (how-tos).

## Common Mistakes

| Mistake | Fix |
|---|---|
| Creating `CHANGELOG.md` | Use `docs/changelogs/{version}.yml` |
| Filename `v0.3.0.yml` | No `v` prefix: `0.3.0.yml` |
| Putting `(#12)` links inside `msg` | Use the `pr:` field; the script builds the link |
| Listing dependency bumps / fmt / lint | Omit — not user-facing |
| Unquoted `msg` starting with a backtick | Quote the whole string |
| Editing rendered `docs/content/releases/*.json` | Generated at release; edit the YAML only |
