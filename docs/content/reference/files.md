---
description: Reference for the files and directories Vibepit uses on the host, including the global and project config, session credentials, and the cache, with their Linux and macOS defaults.
---

# Files and Directories

Vibepit keeps its files in the XDG base directories. To see the paths it
resolves on your machine, run [`vibepit paths`](cli.md#paths):

```
$ vibepit paths
Global config   /home/user/.config/vibepit/config.yaml
Project config  /home/user/my-project/.vibepit/network.yaml
Projects        /home/user/.local/state/vibepit/projects
Sessions        /home/user/.local/state/vibepit/sessions
Cache           /home/user/.cache/vibepit (not found)
```

`(not found)` means the file or directory doesn't exist yet. Vibepit creates
each one when it first needs it. Any other note, such as `(permission denied)`,
means Vibepit can't read the path, and commands that need it fail.

## Overview

| Name | Path | Contents |
|------|------|----------|
| `global-config` | `$XDG_CONFIG_HOME/vibepit/config.yaml` | Settings for every project |
| `project-config` | `<project root>/.vibepit/network.yaml` | Network settings for one project |
| `projects` | `$XDG_STATE_HOME/vibepit/projects/` | Project records for the project roots Vibepit started sessions in |
| `sessions` | `$XDG_STATE_HOME/vibepit/sessions/` | Credentials for running sessions |
| `cache` | `$XDG_CACHE_HOME/vibepit/` | Data Vibepit can recreate |

If an `XDG_*` variable isn't set, Vibepit uses the platform default:

| Variable | Linux default | macOS default |
|----------|---------------|---------------|
| `XDG_CONFIG_HOME` | `~/.config` | `~/Library/Application Support` |
| `XDG_STATE_HOME` | `~/.local/state` | `~/Library/Application Support` |
| `XDG_CACHE_HOME` | `~/.cache` | `~/Library/Caches` |

So on a Mac with no `XDG_*` variables set, the global config is
`~/Library/Application Support/vibepit/config.yaml`.

## Global config

`$XDG_CONFIG_HOME/vibepit/config.yaml` holds settings that apply to every
project, such as allowlist entries, CIDR ranges, the memory limit, and whether
to [prompt for blocked connections](cli.md#blocked-connection-prompt).
[Configure Network Presets](../how-to/configure-presets.md#global-config)
lists the keys.

Vibepit doesn't create this file. To edit it:

```bash
mkdir -p "$(dirname "$(vibepit paths global-config)")"
$EDITOR "$(vibepit paths global-config)"
```

## Project config

`.vibepit/network.yaml` lives in the project root. The project root is the
nearest directory, from the project directory upwards, that holds a `.git`
entry or has a [project record](#project-records). If there is none, or
another user owns the `.git` entry, the project root is the project directory
itself. Inside a Git repository the project root is the repository root,
unless a directory between the two has a project record. The preset selector
writes the file on the first run, and `allow-http`, `allow-dns`, `deny-http`,
and `deny-dns` add entries to it.
[Configure Network Presets](../how-to/configure-presets.md) covers the format.

Inside the sandbox, Vibepit mounts an empty read-only directory over the
project root's `.vibepit`, so code in the sandbox can't read or change the
project config. Code in the sandbox can still write a `.vibepit/network.yaml`
in a subdirectory. When a project root has no project record yet but already
has a project config, `run` and `up` show the file and ask before they use
it.

## Project records

`$XDG_STATE_HOME/vibepit/projects/` holds one project record for each project
root that `run` or `up` started a session in. `run` and `up` write the
project record after you pick presets in the preset selector or confirm an
existing project config. Each project record is a file named after the
SHA-256 hash of the project root's path, and the file contains that path.

The `projects` directory is outside every project, so code in the sandbox
can't change it. A project root with a project record stays the project root
after code in the sandbox deletes its `.git`. The next session then can't
mount a repository further up.

You can delete a project record. The next session in that project root then
shows the project config and asks again before it uses it.

## Session credentials

Each running session has a directory at
`$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`. It holds:

| File | Purpose |
|------|---------|
| `ca.pem`, `client-cert.pem`, `client-key.pem` | mTLS credentials for the proxy's control API |
| `ssh-key`, `ssh-key.pub` | SSH client keypair used by `connect` and `exec` |
| `host-key`, `host-key.pub` | SSH host keypair of the sandbox |
| `maven/settings.xml` | Maven proxy settings mounted into the sandbox |
| `.vibepit/` | The empty directory mounted over the project's `.vibepit` |

Private keys have `0600` permissions. Vibepit deletes the directory when the
session ends. [`down`](cli.md#down) keeps it if it can't remove every
container, so you can retry. The
[security model](../explanations/security-model.md) explains how Vibepit uses
these credentials.

## Cache

`$XDG_CACHE_HOME/vibepit/` holds files Vibepit recreates when they're missing,
so you can delete it at any time:

| Path | Contents |
|------|----------|
| `verified-image-digests` | Image digests whose signatures already passed verification |
| `bin/vibepit-<hash>` | macOS only: the Linux proxy binary extracted from the `vibepit` binary |

## Container volumes

The sandbox home directory and Homebrew live in the `vibepit-home` and
`vibepit-linuxbrew` volumes, not on the host filesystem. See
[Sandbox Environment](sandbox.md#user-and-home-directory).
