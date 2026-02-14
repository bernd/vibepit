# Install Development Tools

The sandbox image ships with common utilities (git, curl, jq, ripgrep, python3,
vim, and others) but does not include language runtimes like Node.js, Go, or
Ruby. You can install additional tools with Homebrew, and they persist across
sessions.

## Install Homebrew

The sandbox includes a `brew` wrapper that installs Homebrew on first use. Run
any `brew` command to trigger the installation:

```bash
brew --version
```

Homebrew is installed into the persistent home volume at
`/home/code/.linuxbrew`, so it only needs to install once. Subsequent sessions
reuse the existing installation.

!!! note
    The `homebrew` and `vcs-github` presets are included in the `default`
    preset, so no additional network configuration is needed.

## Install language runtimes

Use `brew install` to add language runtimes and tools:

```bash
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

```bash
vibepit run --reconfigure
```

Select the relevant preset in the interactive selector (e.g., `pkg-node` for
npm, `pkg-go` for Go modules). See the
[Network Presets](../reference/presets.md) reference for the full list of
available presets.

## What persists between sessions

The sandbox home directory (`/home/code`) is a Docker volume that survives
across sessions. This includes:

- Homebrew and all installed packages
- Shell history and configuration (`.bashrc`, `.profile`)
- Tool configuration files (`.gitconfig`, `.npmrc`, etc.)

Your project directory is bind-mounted from the host and is always up to date.

The root filesystem is read-only, so changes outside of `/home/code`, `/tmp`,
and your project directory do not persist.
