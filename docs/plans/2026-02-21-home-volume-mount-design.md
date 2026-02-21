# Home Volume Mount Restructuring

## Problem

Homebrew on Linux expects to write to `/home/linuxbrew/.linuxbrew`. The current
setup persists only `/home/code` via the `vibepit-home` volume and uses a
symlink (`/home/linuxbrew/.linuxbrew` -> `/home/code/.linuxbrew`) baked into the
image. This causes frequent linking errors because the symlink is part of the
read-only root filesystem and Homebrew's internal paths don't resolve cleanly
through it.

## Solution

Mount `vibepit-home` at `/home` instead of `/home/code`. This makes the entire
`/home` tree writable and persistent, so `/home/linuxbrew/.linuxbrew` can exist
as a real directory on the volume. The symlink is eliminated.

## Changes

### container/client.go

Change `HomeMountPath` from `/home/code` to `/home`. The volume bind becomes
`vibepit-home:/home`.

### image/Dockerfile

- Move the home template from `/home/.code.template` to
  `/opt/vibepit/home-template` (since `/home` is now a mount point and would
  shadow the template).
- Keep `/home/linuxbrew` directory creation (owned by `code` user) so it exists
  with correct permissions in the image layer.
- Remove the symlink (`ln -s ... /home/linuxbrew/.linuxbrew`).
- Remove `/home/code/.linuxbrew` directory creation.

### image/entrypoint.sh

Add migration logic for existing volumes before the existing initialization:

1. Detect old-style volume: check if `.vibepit-initialized` exists at `/home/`
   (the volume root). In old volumes this file sat at the root of the mount,
   which was `/home/code`; now it would appear at `/home/`.
2. Log `"Migrating home volume layout..."`.
3. Create `/home/code/` and move all volume contents into it (excluding `code/`
   itself).
4. If `/home/code/.linuxbrew` exists, move it to `/home/linuxbrew/.linuxbrew`.

After migration (or for fresh volumes), the existing template-copy logic runs
unchanged, just with the template path updated to `/opt/vibepit/home-template`.

### image/bin/brew

Change the clone target from `$HOME/.linuxbrew` to `/home/linuxbrew/.linuxbrew`.
No more indirection.

### image/config/profile

No change needed. Already references `/home/linuxbrew/.linuxbrew/bin/brew`.

## Migration

Existing `vibepit-home` volumes are detected by the presence of
`.vibepit-initialized` at the volume root (`/home/`). The entrypoint moves
everything into a `code/` subdirectory and relocates `.linuxbrew` to
`/home/linuxbrew/.linuxbrew`. A status message is printed during migration.
