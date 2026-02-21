# Home Volume Mount Restructuring — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Mount the vibepit-home volume at `/home` instead of `/home/code` to eliminate the linuxbrew symlink and make all of `/home` writable and persistent.

**Architecture:** Change the single volume mount point, update the container image to store the home template outside `/home`, and add migration logic in the entrypoint for existing volumes.

**Tech Stack:** Go (container client), Bash (entrypoint/scripts), Dockerfile

---

### Task 1: Change volume mount path

**Files:**
- Modify: `container/client.go:45`

**Step 1: Update the constant**

Change:
```go
HomeMountPath     = "/home/code"
```
To:
```go
HomeMountPath     = "/home"
```

**Step 2: Run tests**

Run: `make test`
Expected: PASS (no tests reference this constant directly)

**Step 3: Commit**

```bash
git add container/client.go
git commit -m "Mount vibepit-home volume at /home instead of /home/code"
```

---

### Task 2: Update Dockerfile

**Files:**
- Modify: `image/Dockerfile`

**Step 1: Update the linuxbrew/template section**

Replace lines 74-95 with:

```dockerfile
# Homebrew on Linux needs /home/linuxbrew writable.
RUN install -d -m 0755 "/usr/local/bin" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/${CODE_USER}/.local" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/${CODE_USER}/.local/bin" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/linuxbrew"
```

This removes:
- `/home/code/.linuxbrew` directory creation (no longer needed)
- The symlink `ln -s "/home/${CODE_USER}/.linuxbrew" "/home/linuxbrew/.linuxbrew"`

**Step 2: Move template location**

Replace lines 94-95:
```dockerfile
RUN mv /home/$CODE_USER /home/.${CODE_USER}.template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER
```

With:
```dockerfile
RUN mv /home/$CODE_USER /opt/vibepit/home-template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER
```

The template must live outside `/home` because the volume mount will shadow it.
The `/opt/vibepit` directory is created earlier by `install -d -o root -g root -m 0755 /etc/vibepit` — but that's `/etc/vibepit`. We need to create `/opt/vibepit` too.

Update line 86:
```dockerfile
RUN install -d -o root -g root -m 0755 /etc/vibepit \
  && install -d -o root -g root -m 0755 /opt/vibepit
```

**Step 3: Commit**

```bash
git add image/Dockerfile
git commit -m "Move home template to /opt/vibepit, remove linuxbrew symlink"
```

---

### Task 3: Update entrypoint with migration logic

**Files:**
- Modify: `image/entrypoint.sh`

**Step 1: Rewrite entrypoint.sh**

```bash
#!/bin/bash

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh

home_template="/opt/vibepit/home-template"

# Migrate old-style volume layout.
# Old volumes were mounted at /home/code, so their root contains user files
# (.bashrc, .vibepit-initialized, etc.) directly. Now the volume is mounted at
# /home, so those files appear at /home/ and need to move into /home/code/.
if [ -f "/home/.vibepit-initialized" ]; then
	vp_status "Migrating home volume layout..."
	(
		set -e
		tmp="/home/.migrate-$$"
		mkdir "$tmp"
		cd /home
		shopt -s extglob dotglob
		mv -- !(.migrate-*) "$tmp/"
		mv "$tmp" code
	)
	# Relocate linuxbrew from old path to new path.
	if [ -d "/home/code/.linuxbrew" ]; then
		mkdir -p /home/linuxbrew
		mv /home/code/.linuxbrew /home/linuxbrew/.linuxbrew
	fi
	vp_status "Migration complete."
fi

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	vp_status "Initializing $HOME"
	rsync -aHS "$home_template/" "$HOME/"
	date > "$HOME/.vibepit-initialized"
fi

vp_status "Welcome to the pit!"
vp_status ""

exec /bin/bash --login
```

**Step 2: Commit**

```bash
git add image/entrypoint.sh
git commit -m "Add volume migration and update template path in entrypoint"
```

---

### Task 4: Update brew installer

**Files:**
- Modify: `image/bin/brew`

**Step 1: Change clone target**

Replace:
```bash
git clone "https://github.com/Homebrew/brew.git" "$HOME/.linuxbrew"
```

With:
```bash
git clone "https://github.com/Homebrew/brew.git" "/home/linuxbrew/.linuxbrew"
```

The full file becomes:
```bash
#!/bin/bash

set -eo pipefail

brew_path="/home/linuxbrew/.linuxbrew/bin/brew"

if [ ! -x "$brew_path" ]; then
	echo "+ Installing Homebrew"
	git clone "https://github.com/Homebrew/brew.git" "/home/linuxbrew/.linuxbrew"
fi

if [ -z "$HOMEBREW_PREFIX" ]; then
	eval "$("$brew_path" shellenv)"
fi

exec "$brew_path" "$@"
```

**Step 2: Commit**

```bash
git add image/bin/brew
git commit -m "Clone Homebrew directly to /home/linuxbrew/.linuxbrew"
```

---

### Task 5: Run full test suite

**Step 1: Run unit tests**

Run: `make test`
Expected: PASS

**Step 2: Run integration tests**

Run: `make test-integration`
Expected: PASS (if container runtime available; skip if in sandbox)

**Step 3: Final commit if any fixups needed**
