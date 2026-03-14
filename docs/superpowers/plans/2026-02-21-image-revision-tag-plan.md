# Image Revision Tagging — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace branch-based image tags (`main`) with revision-based tags (`r1`) so old binaries keep working with frozen `main` images while new binaries use `r1`.

**Architecture:** Change the hardcoded image prefix in Go code, update the CI workflow tag, remove the entrypoint mountpoint guard, and update docs.

**Tech Stack:** Go, GitHub Actions, Bash

---

### Task 1: Update image prefix in Go code

**Files:**
- Modify: `cmd/run.go:25`

**Step 1: Change the constant**

Change:
```go
defaultImagePrefix = "ghcr.io/bernd/vibepit:main"
```
To:
```go
defaultImagePrefix = "ghcr.io/bernd/vibepit:r1"
```

**Step 2: Run tests**

Run: `make test`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/run.go
git commit -m "Use revision-based image tag r1 instead of main"
```

---

### Task 2: Update CI workflow

**Files:**
- Modify: `.github/workflows/docker-publish.yml:89`

**Step 1: Replace branch name with revision tag**

Change line 89:
```yaml
tags: "${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:${{ github.ref_name }}-uid-${{ matrix.edition.uid }}-gid-${{ matrix.edition.gid }}"
```
To:
```yaml
tags: "${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:r1-uid-${{ matrix.edition.uid }}-gid-${{ matrix.edition.gid }}"
```

**Step 2: Commit**

```bash
git add .github/workflows/docker-publish.yml
git commit -m "Publish container images with r1 revision tag"
```

---

### Task 3: Remove mountpoint guard from entrypoint

**Files:**
- Modify: `image/entrypoint.sh`

**Step 1: Remove the guard block**

Remove lines 10-15:
```bash
# Guard against new image + old vibepit binary that still mounts at /home/code.
if mountpoint -q /home/code 2>/dev/null && ! mountpoint -q /home 2>/dev/null; then
	echo "ERROR: Home volume is mounted at /home/code but this image expects /home." >&2
	echo "Please update your vibepit binary: https://github.com/bernd/vibepit/releases" >&2
	exit 1
fi
```

**Step 2: Commit**

```bash
git add image/entrypoint.sh
git commit -m "Remove mountpoint guard, revision tags handle compatibility"
```

---

### Task 4: Update docs

**Files:**
- Modify: `docs/content/reference/cli.md:234`
- Modify: `docs/content/how-to/troubleshooting.md:204,212-213`

**Step 1: Update cli.md**

Change `main-uid-1000-gid-1000` to `r1-uid-1000-gid-1000`.

**Step 2: Update troubleshooting.md**

Change all `main-uid-` references to `r1-uid-`.

**Step 3: Commit**

```bash
git add docs/content/reference/cli.md docs/content/how-to/troubleshooting.md
git commit -m "Update docs to reference r1 image revision tag"
```

---

### Task 5: Run full test suite

**Step 1: Run unit tests**

Run: `make test`
Expected: PASS

**Step 2: Run BATS tests**

Run: `bats image/tests/entrypoint-lib.bats`
Expected: PASS
