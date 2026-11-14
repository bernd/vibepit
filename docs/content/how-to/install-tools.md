---
description: Install language runtimes and development tools inside the Vibepit sandbox using Homebrew, with persistent storage across sessions.
---

# Install Development Tools

The sandbox image ships with common utilities (git, curl, jq, ripgrep, python3,
vim, and others) but has no language runtimes like Node.js, Go, or Ruby. You can
install more tools with Homebrew, and they persist across sessions.

## Install Homebrew

The sandbox includes a `brew` wrapper that installs Homebrew on first use. Run
any `brew` command inside the sandbox to start the installation:

```bash title="Inside the sandbox"
brew --version
```

The wrapper installs Homebrew into a persistent volume at
`/home/linuxbrew/.linuxbrew`, so later sessions reuse it.

!!! note
    The `default` preset includes the `homebrew` and `vcs-github` presets. If
    you have `default` enabled (it is pre-selected on first run), you don't need
    to change the network configuration.

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

Installed packages stay in the linuxbrew volume, so future sessions can use
them without reinstalling.

## Network access for package managers

After you install a language runtime, its package manager (npm, pip, cargo,
and so on) needs network access to download dependencies. Enable the matching
preset for your ecosystem:

```bash title="On the host"
vibepit run --reconfigure
```

Select the preset in the interactive selector, for example `pkg-node` for npm or
`pkg-go` for Go modules. The [Network Presets](../reference/presets.md)
reference lists every preset.

## What persists between sessions

Homebrew and its packages live in the linuxbrew volume (`vibepit-linuxbrew`,
mounted at `/home/linuxbrew`). Shell state and dotfiles live in the home volume
(`vibepit-home`, mounted at `/home/code`). Your project directory is a bind
mount from the host, so changes on either side are visible on the other
immediately.

For the full list of mounts and what persists, see the
[Sandbox Environment](../reference/sandbox.md) reference.
