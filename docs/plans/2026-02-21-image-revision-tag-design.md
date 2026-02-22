# Image Revision Tagging

## Problem

The container image tag is branch-based (`main`). When the image changes in a
backwards-incompatible way (e.g. the home volume mount moving from `/home/code`
to `/home`), old binaries pulling the new `main` image will break.

## Solution

Replace the branch-based tag with a revision number (`r1`, `r2`, ...) that is
bumped only on breaking image changes. Old binaries continue using whatever
revision they were built against (the existing `main` tags remain frozen in the
registry). New binaries reference `r1`.

Full image tag format: `ghcr.io/bernd/vibepit:r1-uid-1000-gid-1000`

## Changes

1. **`cmd/run.go`** -- change `defaultImagePrefix` from
   `"ghcr.io/bernd/vibepit:main"` to `"ghcr.io/bernd/vibepit:r1"`.

2. **`.github/workflows/docker-publish.yml`** -- replace `github.ref_name` in
   the image tag with hardcoded `r1`. The trigger stays the same (push to `main`
   when `image/` files change).

3. **`docs/content/reference/cli.md`** -- update example image tags from
   `main-uid-...` to `r1-uid-...`.

4. **`image/entrypoint.sh`** -- remove the `mountpoint` guard added earlier,
   since revision tags solve the compatibility problem.

## Naming

The `r` prefix ("revision") avoids confusion with Vibepit release versions. The
number increments only on breaking image changes, not on every image update.
