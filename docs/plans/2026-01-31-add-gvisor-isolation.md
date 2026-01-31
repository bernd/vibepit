# Adding gVisor to the Sandbox Architecture

## Current State

The sandbox runs untrusted code in Docker containers with hardened flags (`--read-only`, `--cap-drop all`, `--security-opt=no-new-privileges`, non-root user). Network isolation uses a separate proxy container on an internal network.

## Goal

Add gVisor as a syscall isolation layer inside the existing container architecture for defense against kernel exploits. Containers share the host kernel, so kernel vulnerabilities (Dirty COW, Dirty Pipe, etc.) can allow container escapes even with hardened flags. gVisor intercepts syscalls with its own user-space kernel (Sentry), dramatically reducing host kernel attack surface.

## Architecture

```
┌────────────────────────────────────────────────────────────┐
│ Host / Docker VM (macOS)                                   │
│                                                            │
│  ┌─────────────────────┐      ┌─────────────────────┐      │
│  │ External Network    │      │ Internal Network    │      │
│  │                     │      │ (--internal)        │      │
│  │  ┌───────────────┐  │      │                     │      │
│  │  │ Proxy         │◄─┼──────┼────────┐            │      │
│  │  │ Container     │  │      │        │            │      │
│  │  └───────┬───────┘  │      │  ┌─────┴──────────┐ │      │
│  │          │          │      │  │ Outer Container│ │      │
│  │          ▼          │      │  │                │ │      │
│  │      Internet       │      │  │ ┌────────────┐ │ │      │
│  │                     │      │  │ │ gVisor     │ │ │      │
│  └─────────────────────┘      │  │ │ Sandbox    │ │ │      │
│                               │  │ │            │ │ │      │
│                               │  │ │ ┌────────┐ │ │ │      │
│                               │  │ │ │Agent   │ │ │ │      │
│                               │  │ │ └────────┘ │ │ │      │
│                               │  │ └────────────┘ │ │      │
│                               │  └────────────────┘ │      │
│                               └─────────────────────┘      │
└────────────────────────────────────────────────────────────┘
```

## Changes Required

### 1. Outer Container Image

Add `runsc` binary to the container image:

```dockerfile
# Download gVisor binary
ADD https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/runsc /usr/local/bin/runsc
RUN chmod +x /usr/local/bin/runsc
```

The outer container needs additional capabilities to run gVisor. Either `--privileged` or a targeted subset (to be determined).

### 2. OCI Bundle Setup

For each sandbox session, create an OCI bundle:

```
/run/sandbox/<session-id>/
├── config.json
└── rootfs/        # Base filesystem (can be shared across sessions)
```

Minimal `config.json`:

```json
{
  "ociVersion": "1.0.0",
  "process": {
    "terminal": true,
    "user": { "uid": 1000, "gid": 1000 },
    "args": ["/bin/bash"],
    "env": [
      "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
      "TERM=xterm-256color",
      "HTTP_PROXY=http://proxy:3128",
      "HTTPS_PROXY=http://proxy:3128"
    ],
    "cwd": "/workspace"
  },
  "root": {
    "path": "rootfs",
    "readonly": false
  },
  "mounts": [
    {
      "destination": "/proc",
      "type": "proc",
      "source": "proc"
    },
    {
      "destination": "/dev",
      "type": "tmpfs",
      "source": "tmpfs"
    },
    {
      "destination": "/workspace",
      "type": "bind",
      "source": "/path/to/project/in/outer/container",
      "options": ["rbind", "rw"]
    }
  ]
}
```

### 3. Execution Flow

```
Before: docker exec -it <container> bash
After:  docker exec -it <container> runsc run --bundle=/run/sandbox/<id> <id>
```

For interactive sessions with PTY: if the outer container runs with `-it`, gVisor inherits the terminal directly. Set `"terminal": true` in the OCI config and runsc will use the existing PTY.

### 4. Network Configuration

Use gVisor's host network mode (`--network=host` or equivalent in OCI config). This means:

- gVisor uses the outer container's network stack
- The outer container is already isolated on the internal network
- Traffic must go through the proxy container to reach the internet
- Code that ignores `HTTP_PROXY` simply cannot connect

This avoids gVisor's netstack performance overhead while maintaining network isolation.

### 5. Lifecycle Management

```bash
# Create and start sandbox
runsc run --root=/var/run/runsc --bundle=/run/sandbox/<id> <id>

# Or detached with separate exec
runsc create --root=/var/run/runsc --bundle=/run/sandbox/<id> <id>
runsc start <id>
runsc exec --user=1000:1000 <id> /bin/bash

# Cleanup on session end
runsc kill <id>
runsc delete <id>
```

## What Stays the Same

- Proxy container architecture
- Internal/external Docker network split
- Project file bind-mount strategy (adds one more hop: host → outer container → gVisor)
- Overall orchestration model
- Non-root execution

## Open Questions

- [ ] **Rootfs strategy**: Extract from existing container image, or build minimal rootfs?
- [ ] **Minimal capabilities**: What's the smallest capability set for runsc instead of `--privileged`?
- [ ] **Resource limits**: Configure cgroups for CPU/memory limits in OCI config?
- [ ] **Cleanup**: Ensure `runsc delete` runs on session end, even on crashes
- [ ] **macOS testing**: Verify gVisor works correctly inside Docker Desktop's VM

## Security Comparison

| Attack Vector | Plain Container | Container + gVisor |
|---------------|-----------------|-------------------|
| Kernel syscall exploit | Exposed | Mitigated (Sentry intercepts) |
| Container runtime escape | Exposed | Still exposed (outer container) |
| Network exfiltration | Blocked by proxy | Blocked by proxy |
| Filesystem escape | Blocked by mounts | Blocked by mounts + gVisor |
| Resource exhaustion | cgroups | cgroups + gVisor limits |

gVisor doesn't eliminate all risk but significantly raises the bar for kernel-level exploits, which are the primary escape vector for hardened containers.
