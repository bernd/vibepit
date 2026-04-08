# SSH Session Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `up`/`down`/`ssh`/`vibed` commands to decouple container lifecycle from CLI processes and enable multi-session SSH access.

**Architecture:** A `vibed` SSH server (charmbracelet/ssh) runs inside the sandbox container as its entrypoint. The host CLI connects via Go SSH client (golang.org/x/crypto/ssh) through a published port. Ed25519 keypairs generated at `vibepit up` time provide auth and host verification. Session credentials move from `$XDG_RUNTIME_DIR` to `$XDG_STATE_HOME` for durability.

**Tech Stack:** Go, charmbracelet/ssh, creack/pty, golang.org/x/crypto/ssh, Docker API

**Spec:** `docs/superpowers/specs/2026-03-23-ssh-session-architecture-design.md`

---

### Task 1: Move session credentials to `$XDG_STATE_HOME`

**Files:**
- Modify: `cmd/session.go:17-19`
- Test: `cmd/session_test.go` (create if needed)

This is a prerequisite that affects existing functionality. Do it first so all
subsequent work uses the new path.

- [ ] **Step 1: Write failing test for new session base dir**

```go
func TestSessionBaseDirUsesStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/test-vibepit-state")
	base, err := sessionBaseDir()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/test-vibepit-state/vibepit/sessions", base)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestSessionBaseDirUsesStateHome -v`
Expected: FAIL — returns `$XDG_RUNTIME_DIR/vibepit` path

- [ ] **Step 3: Update `sessionBaseDir()` in `cmd/session.go`**

Change lines 17-19 from:
```go
func sessionBaseDir() (string, error) {
	return filepath.Join(xdg.RuntimeDir, config.RuntimeDirName), nil
}
```
To:
```go
func sessionBaseDir() (string, error) {
	return filepath.Join(xdg.StateHome, config.RuntimeDirName, "sessions"), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestSessionBaseDirUsesStateHome -v`
Expected: PASS

- [ ] **Step 5: Run full test suite to verify no regressions**

Run: `make test`
Expected: All tests pass

- [ ] **Step 6: Commit**

```bash
git add cmd/session.go cmd/session_test.go
git commit -m "refactor: move session credentials from XDG_RUNTIME_DIR to XDG_STATE_HOME"
```

---

### Task 2: Add `SessionID` and `Daemon` fields to `SandboxContainerConfig`

**Files:**
- Modify: `container/client.go:598-614` (struct), `container/client.go:669-707` (CreateSandboxContainer)
- Test: `container/client_test.go` (if exists, or create)

- [ ] **Step 1: Add fields to `SandboxContainerConfig` struct**

At `container/client.go:598`, add two fields:

```go
type SandboxContainerConfig struct {
	Image               string
	ProjectDir          string
	WorkDir             string
	RuntimeDir          string
	HomeVolumeName      string
	LinuxbrewVolumeName string
	NetworkID           string
	ProxyIP             string
	ProxyPort           int
	Name                string
	Term                string
	ColorTerm           string
	UID                 int
	User                string
	SessionID           string   // added
	Daemon              bool     // added: when true, creates daemon-mode container
	DaemonBinaryPath    string   // host path to vibepit binary (bind-mounted at /vibepit)
	DaemonHostKeyPath   string   // host path to SSH host key (bind-mounted at /etc/vibepit/sshd/host-key)
	DaemonHostPubPath   string   // host path to SSH host pub key (bind-mounted at /etc/vibepit/sshd/host-key.pub)
	DaemonAuthorizedKey string   // SSH public key for client auth (set as VIBEPIT_SSH_PUBKEY env)
	DaemonEntrypoint    []string // entrypoint override for daemon mode
}
```

- [ ] **Step 2: Add `vibepit.session-id` label to `CreateSandboxContainer`**

In the labels map inside `CreateSandboxContainer` (around line 675), add:

```go
LabelSessionID: cfg.SessionID,
```

- [ ] **Step 3: Add daemon-mode conditional logic**

In `CreateSandboxContainer`, after setting up the base config, add conditional
logic for daemon mode. When `cfg.Daemon` is true:

- Set `Tty: false`, `OpenStdin: false` (instead of `true`)
- Set `NetworkMode: "bridge"` in HostConfig (instead of joining isolated network directly)
- Add `ExposedPorts` for `2222/tcp` and `PortBindings` mapping `2222/tcp` to `127.0.0.1:` (empty host port for dynamic allocation)
- After container creation, call `NetworkConnect` to attach to the isolated session network (same pattern as `StartProxyContainer` lines 493-499)

```go
if cfg.Daemon {
	containerConfig.Tty = false
	containerConfig.OpenStdin = false
	containerConfig.Entrypoint = cfg.DaemonEntrypoint
	containerConfig.Cmd = nil
	containerConfig.Env = append(containerConfig.Env,
		fmt.Sprintf("VIBEPIT_SSH_PUBKEY=%s", cfg.DaemonAuthorizedKey),
	)
	containerConfig.ExposedPorts = nat.PortSet{
		"2222/tcp": struct{}{},
	}
	hostConfig.NetworkMode = "bridge"
	hostConfig.PortBindings = nat.PortMap{
		"2222/tcp": []nat.PortBinding{{HostIP: "127.0.0.1"}},
	}
	// Add daemon-specific bind mounts
	hostConfig.Binds = append(hostConfig.Binds,
		cfg.DaemonBinaryPath+":"+ProxyBinaryPath+":ro",
		cfg.DaemonHostKeyPath+":/etc/vibepit/sshd/host-key:ro",
		cfg.DaemonHostPubPath+":/etc/vibepit/sshd/host-key.pub:ro",
	)
	// Init: true is preserved from the base config (zombie reaping)
	// NetworkingConfig left empty for bridge mode
} else {
	// existing network config for run mode
}
```

After `ContainerCreate`, if daemon mode:
```go
if cfg.Daemon {
	if err := c.docker.NetworkConnect(ctx, cfg.NetworkID, resp.ID, &network.EndpointSettings{}); err != nil {
		return "", fmt.Errorf("connect sandbox to session network: %w", err)
	}
}
```

- [ ] **Step 4: Pass `SessionID` from `RunAction` in `cmd/run.go`**

In `cmd/run.go` around line 288, add `SessionID: sessionID` to the
`SandboxContainerConfig` struct literal.

- [ ] **Step 5: Run tests**

Run: `make test`
Expected: All tests pass

- [ ] **Step 6: Commit**

```bash
git add container/client.go cmd/run.go
git commit -m "feat: add SessionID and Daemon fields to SandboxContainerConfig"
```

---

### Task 3: Add SSH key generation

**Files:**
- Create: `keygen/keygen.go`
- Create: `keygen/keygen_test.go`

- [ ] **Step 1: Write failing test for SSH key generation**

```go
package keygen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestGenerateEd25519Keypair(t *testing.T) {
	priv, pub, err := GenerateEd25519Keypair()
	require.NoError(t, err)
	assert.NotEmpty(t, priv)
	assert.NotEmpty(t, pub)

	// Verify private key is parseable
	signer, err := ssh.ParsePrivateKey(priv)
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519", signer.PublicKey().Type())

	// Verify public key is parseable
	_, _, _, _, err = ssh.ParseAuthorizedKey(pub)
	require.NoError(t, err)
}
```

- [ ] **Step 2: Add dependency**

Run: `go get golang.org/x/crypto`

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./keygen/ -run TestGenerateEd25519Keypair -v`
Expected: FAIL — `GenerateEd25519Keypair` undefined

- [ ] **Step 4: Implement `GenerateEd25519Keypair`**

```go
package keygen

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateEd25519Keypair returns PEM-encoded private key and
// OpenSSH authorized_keys formatted public key.
func GenerateEd25519Keypair() (privateKeyPEM []byte, publicKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	privateKeyPEM = pem.EncodeToMemory(privBytes)

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, fmt.Errorf("create ssh public key: %w", err)
	}
	publicKey = ssh.MarshalAuthorizedKey(sshPub)

	return privateKeyPEM, publicKey, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./keygen/ -run TestGenerateEd25519Keypair -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add keygen/ go.mod go.sum
git commit -m "feat: add Ed25519 SSH keypair generation"
```

---

### Task 4: Add SSH credential writing to session management

**Files:**
- Modify: `cmd/session.go`
- Test: `cmd/session_test.go`

- [ ] **Step 1: Write failing test for SSH credential writing**

```go
func TestWriteSSHCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	sessionID := "test-session-id"
	clientPriv := []byte("fake-client-private-key")
	clientPub := []byte("fake-client-public-key")
	hostPriv := []byte("fake-host-private-key")
	hostPub := []byte("fake-host-public-key")

	err := WriteSSHCredentials(sessionID, clientPriv, clientPub, hostPriv, hostPub)
	require.NoError(t, err)

	sessDir, _ := sessionDir(sessionID)

	data, err := os.ReadFile(filepath.Join(sessDir, "ssh-key"))
	require.NoError(t, err)
	assert.Equal(t, clientPriv, data)

	info, _ := os.Stat(filepath.Join(sessDir, "ssh-key"))
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	data, _ = os.ReadFile(filepath.Join(sessDir, "ssh-key.pub"))
	assert.Equal(t, clientPub, data)

	data, _ = os.ReadFile(filepath.Join(sessDir, "host-key"))
	assert.Equal(t, hostPriv, data)

	info, _ = os.Stat(filepath.Join(sessDir, "host-key"))
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	data, _ = os.ReadFile(filepath.Join(sessDir, "host-key.pub"))
	assert.Equal(t, hostPub, data)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestWriteSSHCredentials -v`
Expected: FAIL — `WriteSSHCredentials` undefined

- [ ] **Step 3: Implement `WriteSSHCredentials`**

Add to `cmd/session.go`:

```go
func WriteSSHCredentials(sessionID string, clientPriv, clientPub, hostPriv, hostPub []byte) error {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	files := map[string]struct {
		data []byte
		perm os.FileMode
	}{
		"ssh-key":      {clientPriv, 0600},
		"ssh-key.pub":  {clientPub, 0644},
		"host-key":     {hostPriv, 0600},
		"host-key.pub": {hostPub, 0644},
	}
	for name, f := range files {
		if err := os.WriteFile(filepath.Join(dir, name), f.data, f.perm); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestWriteSSHCredentials -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/session.go cmd/session_test.go
git commit -m "feat: add SSH credential writing to session management"
```

---

### Task 5: Factor out `init_home` in entrypoint scripts

**Files:**
- Modify: `image/entrypoint-lib.sh`
- Modify: `image/entrypoint.sh`
- Modify: `image/Dockerfile`
- Test: `image/tests/` (BATS tests)

- [ ] **Step 1: Add `init_home` function to `entrypoint-lib.sh`**

Append to `image/entrypoint-lib.sh`:

```bash
init_home() {
	if [ ! -f "$HOME/.vibepit-initialized" ]; then
		vp_status "Initializing $HOME"
		rsync -aHS "/opt/vibepit/home-template/" "$HOME/"
		date > "$HOME/.vibepit-initialized"
	fi
}
```

- [ ] **Step 2: Update `entrypoint.sh` to call `init_home`**

Replace the inline home init logic in `image/entrypoint.sh` (lines 14-18):

From:
```bash
if [ ! -f "$HOME/.vibepit-initialized" ]; then
	vp_status "Initializing $HOME"
	rsync -aHS "/opt/vibepit/home-template/" "$HOME/"
	date > "$HOME/.vibepit-initialized"
fi
```

To:
```bash
init_home
```

- [ ] **Step 3: Add `/etc/vibepit/sshd/` directory to Dockerfile**

In `image/Dockerfile`, after the line that creates `/etc/vibepit` (line 84),
add `sshd` subdirectory:

```dockerfile
RUN install -d -o root -g root -m 0755 /etc/vibepit \
  && install -d -o root -g root -m 0755 /etc/vibepit/sshd \
  && install -d -o root -g root -m 0755 /opt/vibepit
```

- [ ] **Step 4: Run BATS tests**

Run: `make test-bats`
Expected: All tests pass

- [ ] **Step 5: Commit**

```bash
git add image/entrypoint-lib.sh image/entrypoint.sh image/Dockerfile
git commit -m "refactor: factor init_home into entrypoint-lib.sh, add /etc/vibepit/sshd"
```

---

### Task 6: Implement `vibed` SSH server

**Files:**
- Create: `sshd/server.go`
- Create: `sshd/server_test.go`
- Create: `cmd/vibed.go`
- Modify: `cmd/root.go:43-52`

- [ ] **Step 1: Write failing test for SSH server session handling**

```go
package sshd

import (
	"net"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"

	"github.com/bernd/vibepit/keygen"
)

func TestServerAcceptsAuthorizedKey(t *testing.T) {
	hostPriv, hostPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	// Start server on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
	})
	require.NoError(t, err)

	go srv.Serve(listener)
	defer srv.Close()

	// Connect with authorized key
	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)

	hostKey, _, _, _, err := gossh.ParseAuthorizedKey(hostPub)
	require.NoError(t, err)

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.FixedHostKey(hostKey),
	})
	require.NoError(t, err)
	defer client.Close()

	session, err := client.NewSession()
	require.NoError(t, err)
	defer session.Close()

	output, err := session.Output("echo hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(output))
}

func TestServerRejectsUnauthorizedKey(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	_, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	unauthorizedPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
	})
	require.NoError(t, err)

	go srv.Serve(listener)
	defer srv.Close()

	signer, err := gossh.ParsePrivateKey(unauthorizedPriv)
	require.NoError(t, err)

	_, err = gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sshd/ -run TestServer -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement the SSH server**

Create `sshd/server.go`:

```go
package sshd

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"

	"github.com/creack/pty"

	charmssh "github.com/charmbracelet/ssh"
)

type Config struct {
	HostKeyPEM    []byte
	AuthorizedKey []byte
}

type Server struct {
	server *charmssh.Server
}

func NewServer(cfg Config) (*Server, error) {
	authorizedKey, _, _, _, err := gossh.ParseAuthorizedKey(cfg.AuthorizedKey)
	if err != nil {
		return nil, fmt.Errorf("parse authorized key: %w", err)
	}

	srv, err := charmssh.NewServer(
		charmssh.HostKeyPEM(cfg.HostKeyPEM),
		charmssh.PublicKeyAuth(func(ctx charmssh.Context, key charmssh.PublicKey) bool {
			return charmssh.KeysEqual(key, authorizedKey)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create ssh server: %w", err)
	}

	srv.Handler = handleSession

	return &Server{server: srv}, nil
}

func (s *Server) Serve(l net.Listener) error {
	return s.server.Serve(l)
}

func (s *Server) Close() error {
	return s.server.Close()
}

func handleSession(sess charmssh.Session) {
	ptyReq, winCh, isPty := sess.Pty()

	if isPty {
		handlePTYSession(sess, ptyReq, winCh)
	} else {
		handleExecSession(sess)
	}
}

func handlePTYSession(sess charmssh.Session, ptyReq charmssh.Pty, winCh <-chan charmssh.Window) {
	cmd := exec.Command("/bin/bash", "--login")
	cmd.Env = sess.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(ptyReq.Window.Height),
		Cols: uint16(ptyReq.Window.Width),
	})
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "failed to start shell: %s\n", err)
		sess.Exit(1)
		return
	}
	defer ptmx.Close()

	// Handle window resize using pty.Setsize (portable)
	go func() {
		for win := range winCh {
			pty.Setsize(ptmx, &pty.Winsize{
				Rows: uint16(win.Height),
				Cols: uint16(win.Width),
			})
		}
	}()

	// Bidirectional copy
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(ptmx, sess)
	}()
	go func() {
		defer wg.Done()
		io.Copy(sess, ptmx)
	}()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			sess.Exit(exitErr.ExitCode())
			wg.Wait()
			return
		}
	}
	wg.Wait()
	sess.Exit(0)
}

func handleExecSession(sess charmssh.Session) {
	args := sess.Command()
	if len(args) == 0 {
		fmt.Fprintln(sess.Stderr(), "no command specified")
		sess.Exit(1)
		return
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = sess.Environ()
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	cmd.Stdin = sess

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			sess.Exit(exitErr.ExitCode())
			return
		}
		fmt.Fprintf(sess.Stderr(), "failed to run command: %s\n", err)
		sess.Exit(1)
		return
	}
	sess.Exit(0)
}

```

**Implementation caveat:** Verify how `charmbracelet/ssh`'s `sess.Command()`
parses the SSH exec request string. If it does naive whitespace splitting,
the shell-quoting in `vibepit ssh` will produce literal quote characters in
argv. In that case, the server-side handler should use `sess.RawCommand()` and
parse with `shellwords` or similar. Test this during Step 5 with arguments
containing spaces.

- [ ] **Step 4: Add dependencies**

Run: `go get github.com/charmbracelet/ssh && go get github.com/creack/pty`

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./sshd/ -run TestServer -v`
Expected: PASS

- [ ] **Step 6: Create `cmd/vibed.go` command**

```go
package cmd

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/bernd/vibepit/sshd"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func VibedCommand() *cli.Command {
	return &cli.Command{
		Name:   "vibed",
		Usage:  "Run the SSH server (internal, runs inside sandbox)",
		Hidden: true,
		Action: VibedAction,
	}
}

func VibedAction(ctx context.Context, cmd *cli.Command) error {
	hostKeyPath := "/etc/vibepit/sshd/host-key"
	hostKey, err := os.ReadFile(hostKeyPath)
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	authorizedKey := os.Getenv("VIBEPIT_SSH_PUBKEY")
	if authorizedKey == "" {
		return fmt.Errorf("VIBEPIT_SSH_PUBKEY not set")
	}

	srv, err := sshd.NewServer(sshd.Config{
		HostKeyPEM:    hostKey,
		AuthorizedKey: []byte(authorizedKey),
	})
	if err != nil {
		return fmt.Errorf("create ssh server: %w", err)
	}

	listener, err := net.Listen("tcp", "0.0.0.0:2222")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	tui.Status("Listening", "on :2222")
	return srv.Serve(listener)
}
```

- [ ] **Step 7: Register `vibed` command in `cmd/root.go`**

Add `VibedCommand()` to the `Commands` slice (around line 50):

```go
Commands: []*cli.Command{
	RunCommand(),
	AllowHTTPCommand(),
	AllowDNSCommand(),
	ProxyCommand(),
	VibedCommand(), // added
	SessionsCommand(),
	MonitorCommand(),
	UpdateCommand(),
},
```

- [ ] **Step 8: Run full test suite**

Run: `make test`
Expected: All tests pass

- [ ] **Step 9: Commit**

```bash
git add sshd/ cmd/vibed.go cmd/root.go go.mod go.sum
git commit -m "feat: add vibed SSH server and command"
```

---

### Task 7: Implement `vibepit up` command

**Files:**
- Create: `cmd/up.go`
- Modify: `cmd/root.go`
- Modify: `container/client.go` (add `StartContainer` and `FindPublishedPort` methods)

This task extracts the shared setup logic from `RunAction` and creates the `up`
command. The `up` command reuses the same setup flow but creates the sandbox in
daemon mode and does not attach.

- [ ] **Step 1: Add `NoRestart` field to `ProxyContainerConfig` in `container/client.go`**

Add `NoRestart bool` to the `ProxyContainerConfig` struct (around line 413).
In `StartProxyContainer`, make the restart policy conditional (around line 477):

```go
if !cfg.NoRestart {
	hostConfig.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
}
```

- [ ] **Step 2: Add `StartContainer` method to `container/client.go`**

```go
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	return c.docker.ContainerStart(ctx, containerID, container.StartOptions{})
}
```

- [ ] **Step 2: Add `FindPublishedPort` method to `container/client.go`**

```go
func (c *Client) FindPublishedPort(ctx context.Context, containerID string, containerPort string) (int, error) {
	info, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, fmt.Errorf("inspect container: %w", err)
	}
	bindings, ok := info.NetworkSettings.Ports[nat.Port(containerPort)]
	if !ok || len(bindings) == 0 {
		return 0, fmt.Errorf("port %s not published", containerPort)
	}
	var port int
	if _, err := fmt.Sscanf(bindings[0].HostPort, "%d", &port); err != nil {
		return 0, fmt.Errorf("parse host port: %w", err)
	}
	return port, nil
}
```

- [ ] **Step 3: Create `cmd/up.go`**

Note: use the existing `FindRunningSession` method from `container/client.go:199`
for sandbox discovery — it already filters by `LabelProjectDir` and `RoleSandbox`.

The `UpAction` follows the same structure as `RunAction` (lines 76-317 of
`cmd/run.go`) but:
- Uses `Daemon: true` in `SandboxContainerConfig`
- Generates SSH keypairs (client + host) and writes them via `WriteSSHCredentials`
- Passes `VIBEPIT_SSH_PUBKEY` env var and host key bind mounts to sandbox config
- Bind-mounts the vibepit binary into the sandbox
- Starts containers without attaching
- Removes `RestartPolicyUnlessStopped` from proxy for `up` sessions
- Checks for existing session first (idempotent)
- Does not defer cleanup

```go
package cmd

import (
	// ... imports
)

func UpCommand() *cli.Command {
	return &cli.Command{
		Name:  "up",
		Usage: "Start sandbox and proxy containers",
		Flags: []cli.Flag{
			// Reuse relevant flags from run command
		},
		Action: UpAction,
	}
}

func UpAction(ctx context.Context, cmd *cli.Command) error {
	// 1. Resolve project root — use the same inline logic as RunAction
	//    (cmd/run.go:77-98): resolve cwd, navigate to git root if available
	// 2. Load config (same as RunAction)
	// 3. Check for existing session
	existing, err := client.FindRunningSession(ctx, projectRoot)
	if existing != "" {
		tui.Status("Session", "already running")
		return nil
	}

	// 4. Image preparation (same as RunAction)
	// 5. Generate session ID, create network (same as RunAction)
	// 6. Generate mTLS credentials (same as RunAction)
	// 7. Generate SSH keypairs
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	// ... error handling
	hostPriv, hostPub, err := keygen.GenerateEd25519Keypair()
	// ... error handling
	if err := WriteSSHCredentials(sessionID, clientPriv, clientPub, hostPriv, hostPub); err != nil {
		return fmt.Errorf("write ssh credentials: %w", err)
	}

	// 8. Start proxy (same as RunAction, but without restart policy)
	// 9. Create sandbox in daemon mode
	sandboxContainer, err := client.CreateSandboxContainer(ctx, container.SandboxContainerConfig{
		// ... same fields as RunAction ...
		SessionID: sessionID,
		Daemon:    true,
		// Additional: SSH host key mounts, VIBEPIT_SSH_PUBKEY env, vibepit binary mount
	})

	// 10. Start sandbox
	if err := client.StartContainer(ctx, sandboxContainer); err != nil {
		return fmt.Errorf("start sandbox: %w", err)
	}

	tui.Status("Started", "session %s", sessionID)
	return nil
}
```

Implementation notes:

Significant code will be shared between `RunAction` and `UpAction`. Extract
shared setup into helper functions as needed to avoid duplication. The key
differences are:
- `UpAction` uses daemon mode (no TTY, SSH port publishing, overridden entrypoint)
- `UpAction` generates SSH keys
- `UpAction` does not attach or defer cleanup
- `UpAction` removes proxy restart policy

For the proxy restart policy: `StartProxyContainer` in `container/client.go:477`
hardcodes `RestartPolicyUnlessStopped`. Add a `NoRestart bool` field to
`ProxyContainerConfig`. When true, omit the restart policy. `UpAction` sets
`NoRestart: true`; `RunAction` leaves it false for backward compatibility.

- [ ] **Step 5: Register `up` command in `cmd/root.go`**

Add `UpCommand()` to the `Commands` slice.

- [ ] **Step 6: Run full test suite**

Run: `make test`
Expected: All tests pass

- [ ] **Step 7: Commit**

```bash
git add cmd/up.go cmd/root.go container/client.go
git commit -m "feat: add vibepit up command"
```

---

### Task 8: Implement `vibepit down` command

**Files:**
- Create: `cmd/down.go`
- Modify: `cmd/root.go`
- Modify: `container/client.go` (add `FindContainersBySessionID`, `ContainerNetworks` methods)

- [ ] **Step 1: Add `FindContainersBySessionID` to `container/client.go`**

```go
func (c *Client) FindContainersBySessionID(ctx context.Context, sessionID string) ([]string, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("%s=%s", LabelSessionID, sessionID)),
		),
	})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range containers {
		ids = append(ids, c.ID)
	}
	return ids, nil
}
```

- [ ] **Step 2: Add `SessionIDFromContainer` to `container/client.go`**

```go
func (c *Client) SessionIDFromContainer(ctx context.Context, containerID string) (string, error) {
	info, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	sid, ok := info.Config.Labels[LabelSessionID]
	if !ok {
		return "", fmt.Errorf("container has no session ID label")
	}
	return sid, nil
}
```

- [ ] **Step 3: Create `cmd/down.go`**

```go
package cmd

import (
	"fmt"

	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func DownCommand() *cli.Command {
	return &cli.Command{
		Name:  "down",
		Usage: "Stop and remove sandbox and proxy containers",
		Action: DownAction,
	}
}

func DownAction(ctx context.Context, cmd *cli.Command) error {
	client, err := container.NewClient()
	if err != nil {
		return err
	}

	// Resolve project root — same inline logic as RunAction (cmd/run.go:77-98)
	projectRoot := /* ... */

	// Find sandbox for this project (FindRunningSession filters by role=sandbox)
	sandboxID, _ := client.FindRunningSession(ctx, projectRoot)
	if sandboxID == "" {
		return fmt.Errorf("no running session found for %s", projectRoot)
	}

	sessionID, err := client.SessionIDFromContainer(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("get session ID: %w", err)
	}

	// Find all containers (sandbox + proxy) with this session ID
	containers, _ := client.FindContainersBySessionID(ctx, sessionID)

	// Best-effort cleanup: stop and remove all containers
	for _, id := range containers {
		tui.Status("Stopping", "container %s", id[:12])
		client.StopAndRemove(ctx, id)
	}

	// Remove network
	networkName := fmt.Sprintf("vibepit-net-%s", sessionID)
	client.RemoveNetwork(ctx, networkName)

	// Remove credentials
	CleanupSessionCredentials(sessionID)

	tui.Status("Stopped", "session %s", sessionID)
	return nil
}
```

- [ ] **Step 4: Register `down` command in `cmd/root.go`**

Add `DownCommand()` to the `Commands` slice.

- [ ] **Step 5: Run full test suite**

Run: `make test`
Expected: All tests pass

- [ ] **Step 6: Commit**

```bash
git add cmd/down.go cmd/root.go container/client.go
git commit -m "feat: add vibepit down command"
```

---

### Task 9: Implement `vibepit ssh` command

**Files:**
- Create: `cmd/ssh.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Create `cmd/ssh.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// shellescape quotes a string for safe transmission over SSH wire protocol.
func shellescape(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == ',' || c == '+' || c == '=') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func SSHCommand() *cli.Command {
	return &cli.Command{
		Name:  "ssh",
		Usage: "Connect to running sandbox via SSH",
		Action: SSHAction,
	}
}

func SSHAction(ctx context.Context, cmd *cli.Command) error {
	client, err := container.NewClient()
	if err != nil {
		return err
	}

	// Resolve project root — same inline logic as RunAction (cmd/run.go:77-98)
	projectRoot := /* ... */

	// Discover sandbox
	sandboxID, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if sandboxID == "" {
		return fmt.Errorf("no running sandbox found — run 'vibepit up' first")
	}

	// Get session ID and published port
	sessionID, err := client.SessionIDFromContainer(ctx, sandboxID)
	if err != nil {
		return err
	}
	port, err := client.FindPublishedPort(ctx, sandboxID, "2222/tcp")
	if err != nil {
		return err
	}

	// Load credentials
	sessDir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	privateKey, err := os.ReadFile(filepath.Join(sessDir, "ssh-key"))
	if err != nil {
		return fmt.Errorf("read ssh key: %w (credentials missing — run 'vibepit down && vibepit up')", err)
	}
	hostPubKey, err := os.ReadFile(filepath.Join(sessDir, "host-key.pub"))
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	// Parse keys
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPubKey)
	if err != nil {
		return fmt.Errorf("parse host key: %w", err)
	}

	// Connect
	conn, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &ssh.ClientConfig{
		User:            "code",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
	})
	if err != nil {
		return fmt.Errorf("ssh connect: %w", err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Command mode — argv semantics per spec. The SSH wire protocol sends a
	// single command string. charmbracelet/ssh server splits it back into argv
	// via sess.Command(). We shell-quote each argument to preserve spaces and
	// special characters across the wire.
	cmdArgs := cmd.Args().Tail()
	if len(cmdArgs) > 0 {
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		session.Stdin = os.Stdin
		// Shell-quote arguments to preserve argv semantics across SSH wire protocol
		quoted := make([]string, len(cmdArgs))
		for i, arg := range cmdArgs {
			quoted[i] = shellescape(arg)
		}
		if err := session.Run(strings.Join(quoted, " ")); err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				os.Exit(exitErr.ExitStatus())
			}
			return err
		}
		return nil
	}

	// Interactive mode
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState)

	w, h, _ := term.GetSize(fd)
	termEnv := os.Getenv("TERM")
	if termEnv == "" {
		termEnv = "xterm-256color"
	}

	if err := session.RequestPty(termEnv, h, w, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	session.Stdin = os.Stdin

	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	// Forward SIGWINCH
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			if w, h, err := term.GetSize(fd); err == nil {
				session.WindowChange(h, w)
			}
		}
	}()

	return session.Wait()
}
```

- [ ] **Step 2: Register `ssh` command in `cmd/root.go`**

Add `SSHCommand()` to the `Commands` slice.

- [ ] **Step 3: Run full test suite**

Run: `make test`
Expected: All tests pass

- [ ] **Step 4: Commit**

```bash
git add cmd/ssh.go cmd/root.go
git commit -m "feat: add vibepit ssh command"
```

---

### Task 10: Integration testing

**Files:**
- Test manually or via integration test

This task verifies the full end-to-end flow. It requires Docker access and
cannot run inside a nested vibepit sandbox.

- [ ] **Step 1: Build and test `vibepit up`**

Run from a project directory on the host:
```bash
go run . up
```
Expected: Proxy and sandbox containers start, session ID printed.

- [ ] **Step 2: Verify containers are running**

```bash
go run . sessions
docker ps --filter label=vibepit
```
Expected: Both proxy and sandbox containers listed.

- [ ] **Step 3: Test `vibepit ssh` interactive mode**

```bash
go run . ssh
```
Expected: Login shell inside sandbox. Verify:
- Terminal resize works (resize window)
- `exit` cleanly disconnects

- [ ] **Step 4: Test `vibepit ssh` command mode**

```bash
go run . ssh -- echo hello
go run . ssh -- whoami
```
Expected: `hello` and `code` respectively.

- [ ] **Step 5: Test multiple sessions**

Open two terminals, run `go run . ssh` in both.
Expected: Both get independent shell sessions.

- [ ] **Step 6: Test `vibepit down`**

```bash
go run . down
docker ps --filter label=vibepit
```
Expected: All containers stopped and removed. No vibepit containers listed.

- [ ] **Step 7: Test idempotent `vibepit up`**

```bash
go run . up
go run . up
```
Expected: Second `up` prints "session already running" and exits 0.

- [ ] **Step 8: Commit any fixes from integration testing**

```bash
git add -A
git commit -m "fix: integration test fixes for SSH session architecture"
```
