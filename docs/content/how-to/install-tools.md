---
description: Install language runtimes and development tools inside the Vibepit sandbox using Homebrew, with persistent storage across sessions.
---

# Install Development Tools

The sandbox image ships with common utilities (git, curl, jq, ripgrep, python3,
vim, and others) but does not include language runtimes like Node.js, Go, or
Ruby. You can install additional tools with Homebrew, and they persist across
sessions.

## Install Homebrew

The sandbox includes a `brew` wrapper that installs Homebrew on first use. Run
any `brew` command inside the sandbox to trigger the installation:

```bash title="Inside the sandbox"
brew --version
```

Homebrew is installed into the persistent home volume at
`/home/linuxbrew/.linuxbrew`, so it only needs to install once. Subsequent
sessions reuse the existing installation.

!!! note
    The `homebrew` and `vcs-github` presets are included in the `default`
    preset. If you have `default` enabled (pre-selected on first run), no
    additional network configuration is needed.

## Install language runtimes

Use `brew install` inside the sandbox to add language runtimes and tools:

```bash title="Inside the sandbox"
# Node.js
brew install node

# Go
brew install go

# Ruby
brew install ruby

# Rust
brew install rust
```

Installed packages persist in the home volume and are available in future
sessions without reinstalling.

## Network access for package managers

After installing a language runtime, its package manager (npm, pip, cargo, etc.)
needs network access to download dependencies. Enable the matching preset for
your ecosystem:

```bash title="On the host"
vibepit run --reconfigure
```

Select the relevant preset in the interactive selector (e.g., `pkg-node` for
npm, `pkg-go` for Go modules). See the
[Network Presets](../reference/presets.md) reference for the full list of
available presets.

## What persists between sessions

Homebrew and all installed packages live in the persistent home volume
(`/home/code`), so they survive across sessions. Your project directory is
bind-mounted from the host and is always up to date.

For the full list of mounts and what persists, see the
[Sandbox Environment](../reference/sandbox.md) reference.
