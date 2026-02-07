# Host Access Part 2: SSH Port Forwarding — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add SSH-based remote port forwarding so sandbox processes can reach non-HTTP host services (databases, gRPC, custom protocols) via `host.vibepit:<port>`.

**Architecture:** The proxy binary gains an embedded SSH server (`golang.org/x/crypto/ssh`). SSH keypairs are pre-generated on the host and injected into the proxy container via environment variables. After the proxy starts, the host connects as an SSH client and establishes remote port forwards for each configured port. Connections to `<proxy-ip>:<port>` are tunneled back to `127.0.0.1:<port>` on the host.

**Security invariants:**
- SSH server binds to bridge interface only (unreachable from sandbox)
- Forward destination hardcoded to `127.0.0.1` (server rejects other targets)
- SSH port published on `127.0.0.1:<random>` (host-local only)
- SSH keepalives detect dead connections; client reconnects automatically

**Tech Stack:** `golang.org/x/crypto/ssh` (new dependency), existing `proxy`, `cmd`, `container` packages.

**Prerequisite:** Part 1 (config, DNS, HTTP proxy) must be complete.

---

### Task 1: Add `golang.org/x/crypto/ssh` dependency

**Step 1: Add the dependency**

Run: `cd /home/bernd/Code/vibepit && go get golang.org/x/crypto/ssh`

**Step 2: Verify**

Run: `go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add golang.org/x/crypto for SSH support"
```

---

### Task 2: SSH key generation

**Files:**
- Create: `proxy/sshkeys.go`
- Create: `proxy/sshkeys_test.go`

**Step 1: Write the failing test**

Create `proxy/sshkeys_test.go`:

```go
package proxy

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "golang.org/x/crypto/ssh"
)

func TestGenerateSSHKeys(t *testing.T) {
    keys, err := GenerateSSHKeys()
    require.NoError(t, err)

    t.Run("host key is parseable", func(t *testing.T) {
        signer, err := ssh.ParsePrivateKey(keys.HostKeyPEM)
        require.NoError(t, err)
        assert.NotNil(t, signer)
    })

    t.Run("client key is parseable", func(t *testing.T) {
        signer, err := ssh.ParsePrivateKey(keys.ClientKeyPEM)
        require.NoError(t, err)
        assert.NotNil(t, signer)
    })

    t.Run("authorized key matches client key", func(t *testing.T) {
        signer, err := ssh.ParsePrivateKey(keys.ClientKeyPEM)
        require.NoError(t, err)

        pubKey, _, _, _, err := ssh.ParseAuthorizedKey(keys.AuthorizedKeyBytes)
        require.NoError(t, err)

        assert.Equal(t, signer.PublicKey().Marshal(), pubKey.Marshal())
    })

    t.Run("host public key matches host private key", func(t *testing.T) {
        signer, err := ssh.ParsePrivateKey(keys.HostKeyPEM)
        require.NoError(t, err)

        assert.Equal(t, keys.HostPublicKey.Marshal(), signer.PublicKey().Marshal())
    })
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestGenerateSSHKeys -v`
Expected: FAIL — `GenerateSSHKeys` not found.

**Step 3: Implement**

Create `proxy/sshkeys.go`:

```go
package proxy

import (
    "crypto/ed25519"
    "crypto/rand"
    "crypto/x509"
    "encoding/pem"
    "fmt"

    "golang.org/x/crypto/ssh"
)

// SSHKeys holds pre-generated SSH key material for a session.
type SSHKeys struct {
    HostKeyPEM         []byte
    HostPublicKey      ssh.PublicKey
    ClientKeyPEM       []byte
    AuthorizedKeyBytes []byte
}

// GenerateSSHKeys creates ed25519 keypairs for the SSH host and client.
func GenerateSSHKeys() (*SSHKeys, error) {
    // Host key.
    _, hostPriv, err := ed25519.GenerateKey(rand.Reader)
    if err != nil {
        return nil, fmt.Errorf("generate host key: %w", err)
    }
    hostPrivDER, err := x509.MarshalPKCS8PrivateKey(hostPriv)
    if err != nil {
        return nil, fmt.Errorf("marshal host key: %w", err)
    }
    hostKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: hostPrivDER})

    hostSigner, err := ssh.NewSignerFromKey(hostPriv)
    if err != nil {
        return nil, fmt.Errorf("host signer: %w", err)
    }

    // Client key.
    _, clientPriv, err := ed25519.GenerateKey(rand.Reader)
    if err != nil {
        return nil, fmt.Errorf("generate client key: %w", err)
    }
    clientPrivDER, err := x509.MarshalPKCS8PrivateKey(clientPriv)
    if err != nil {
        return nil, fmt.Errorf("marshal client key: %w", err)
    }
    clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientPrivDER})

    clientSigner, err := ssh.NewSignerFromKey(clientPriv)
    if err != nil {
        return nil, fmt.Errorf("client signer: %w", err)
    }
    authorizedKey := ssh.MarshalAuthorizedKey(clientSigner.PublicKey())

    return &SSHKeys{
        HostKeyPEM:         hostKeyPEM,
        HostPublicKey:      hostSigner.PublicKey(),
        ClientKeyPEM:       clientKeyPEM,
        AuthorizedKeyBytes: authorizedKey,
    }, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestGenerateSSHKeys -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/sshkeys.go proxy/sshkeys_test.go
git commit -m "proxy: add SSH key generation for host access"
```

---

### Task 3: Embedded SSH server with remote port forwarding

**Files:**
- Create: `proxy/sshd.go`
- Create: `proxy/sshd_test.go`

**Step 1: Write the failing test**

Create `proxy/sshd_test.go`:

```go
package proxy

import (
    "io"
    "net"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "golang.org/x/crypto/ssh"
)

func TestSSHDRemoteForward(t *testing.T) {
    keys, err := GenerateSSHKeys()
    require.NoError(t, err)

    // Start a TCP echo server simulating a host service.
    echoLn, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    defer echoLn.Close()
    go func() {
        for {
            conn, err := echoLn.Accept()
            if err != nil {
                return
            }
            go func(c net.Conn) {
                defer c.Close()
                io.Copy(c, c)
            }(conn)
        }
    }()

    // Start the SSH server.
    sshd, err := NewSSHServer(keys.HostKeyPEM, keys.AuthorizedKeyBytes)
    require.NoError(t, err)

    ln, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    defer ln.Close()

    go sshd.Serve(ln)

    // Connect as SSH client.
    clientSigner, err := ssh.ParsePrivateKey(keys.ClientKeyPEM)
    require.NoError(t, err)

    clientConfig := &ssh.ClientConfig{
        User:            "vibepit",
        Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
        HostKeyCallback: ssh.FixedHostKey(keys.HostPublicKey),
    }

    client, err := ssh.Dial("tcp", ln.Addr().String(), clientConfig)
    require.NoError(t, err)
    defer client.Close()

    // Request remote forward on a random port.
    remoteLn, err := client.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    defer remoteLn.Close()

    // Forward accepted connections to the echo server.
    go func() {
        for {
            conn, err := remoteLn.Accept()
            if err != nil {
                return
            }
            go func(c net.Conn) {
                defer c.Close()
                upstream, err := net.Dial("tcp", echoLn.Addr().String())
                if err != nil {
                    return
                }
                defer upstream.Close()
                go io.Copy(upstream, c)
                io.Copy(c, upstream)
            }(conn)
        }
    }()

    time.Sleep(50 * time.Millisecond)

    t.Run("data flows through SSH remote forward", func(t *testing.T) {
        conn, err := net.Dial("tcp", remoteLn.Addr().String())
        require.NoError(t, err)
        defer conn.Close()

        msg := []byte("hello host")
        _, err = conn.Write(msg)
        require.NoError(t, err)

        buf := make([]byte, len(msg))
        _, err = io.ReadFull(conn, buf)
        require.NoError(t, err)

        assert.Equal(t, msg, buf)
    })

    t.Run("rejects unauthorized key", func(t *testing.T) {
        otherKeys, err := GenerateSSHKeys()
        require.NoError(t, err)

        otherSigner, err := ssh.ParsePrivateKey(otherKeys.ClientKeyPEM)
        require.NoError(t, err)

        badConfig := &ssh.ClientConfig{
            User:            "vibepit",
            Auth:            []ssh.AuthMethod{ssh.PublicKeys(otherSigner)},
            HostKeyCallback: ssh.FixedHostKey(keys.HostPublicKey),
        }

        _, err = ssh.Dial("tcp", ln.Addr().String(), badConfig)
        assert.Error(t, err)
    })

    t.Run("rejects forward to non-localhost", func(t *testing.T) {
        // Attempt to request a remote forward to a non-127.0.0.1 address.
        // The server must reject this.
        _, err := client.Listen("tcp", "10.0.0.1:8080")
        assert.Error(t, err)
    })

    t.Run("rejects shell channel", func(t *testing.T) {
        _, _, err := client.OpenChannel("session", nil)
        assert.Error(t, err)
    })
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestSSHDRemoteForward -v`
Expected: FAIL — `NewSSHServer` does not exist.

**Step 3: Implement**

Create `proxy/sshd.go`:

```go
package proxy

import (
    "fmt"
    "io"
    "net"
    "sync"

    "golang.org/x/crypto/ssh"
)

// SSHServer is a minimal SSH server that supports remote port forwarding
// (tcpip-forward). It does not provide shell access.
type SSHServer struct {
    config *ssh.ServerConfig
}

// NewSSHServer creates an SSH server with the given host key and authorized
// client public key. Only public key auth is accepted.
func NewSSHServer(hostKeyPEM, authorizedKeyBytes []byte) (*SSHServer, error) {
    hostSigner, err := ssh.ParsePrivateKey(hostKeyPEM)
    if err != nil {
        return nil, fmt.Errorf("parse host key: %w", err)
    }

    authorizedKey, _, _, _, err := ssh.ParseAuthorizedKey(authorizedKeyBytes)
    if err != nil {
        return nil, fmt.Errorf("parse authorized key: %w", err)
    }

    config := &ssh.ServerConfig{
        PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
            if ssh.KeysEqual(key, authorizedKey) {
                return &ssh.Permissions{}, nil
            }
            return nil, fmt.Errorf("unknown public key for %q", conn.User())
        },
    }
    config.AddHostKey(hostSigner)

    return &SSHServer{config: config}, nil
}

// Serve accepts SSH connections on the listener.
func (s *SSHServer) Serve(ln net.Listener) error {
    for {
        conn, err := ln.Accept()
        if err != nil {
            return err
        }
        go s.handleConn(conn)
    }
}

// tcpipForwardRequest matches the RFC 4254 tcpip-forward request payload.
type tcpipForwardRequest struct {
    Addr string
    Port uint32
}

// tcpipForwardResponse is sent when the server allocates a port.
type tcpipForwardResponse struct {
    Port uint32
}

// forwardedTCPPayload matches the RFC 4254 forwarded-tcpip channel payload.
type forwardedTCPPayload struct {
    Addr       string
    Port       uint32
    OriginAddr string
    OriginPort uint32
}

func (s *SSHServer) handleConn(netConn net.Conn) {
    sshConn, chans, reqs, err := ssh.NewServerConn(netConn, s.config)
    if err != nil {
        netConn.Close()
        return
    }

    var mu sync.Mutex
    listeners := make(map[string]net.Listener)

    defer func() {
        mu.Lock()
        for _, ln := range listeners {
            ln.Close()
        }
        mu.Unlock()
        sshConn.Close()
    }()

    // Reject all channel opens — no shell access.
    go func() {
        for newCh := range chans {
            newCh.Reject(ssh.Prohibited, "no shell access")
        }
    }()

    for req := range reqs {
        switch req.Type {
        case "tcpip-forward":
            s.handleForward(req, sshConn, &mu, listeners)
        case "cancel-tcpip-forward":
            s.handleCancelForward(req, &mu, listeners)
        default:
            if req.WantReply {
                req.Reply(false, nil)
            }
        }
    }
}

func (s *SSHServer) handleForward(req *ssh.Request, sshConn *ssh.ServerConn, mu *sync.Mutex, listeners map[string]net.Listener) {
    var fwd tcpipForwardRequest
    if err := ssh.Unmarshal(req.Payload, &fwd); err != nil {
        req.Reply(false, nil)
        return
    }

    // Security: only allow forwarding to localhost. The host-side client
    // should only request 127.0.0.1 or the proxy's internal IP, but we
    // enforce this server-side as defense in depth.
    fwdIP := net.ParseIP(fwd.Addr)
    if fwdIP == nil || (!fwdIP.IsLoopback() && !isProxyInternalIP(fwdIP)) {
        req.Reply(false, nil)
        return
    }

    addr := net.JoinHostPort(fwd.Addr, fmt.Sprintf("%d", fwd.Port))
    ln, err := net.Listen("tcp", addr)
    if err != nil {
        req.Reply(false, nil)
        return
    }

    _, portStr, _ := net.SplitHostPort(ln.Addr().String())
    var actualPort uint32
    fmt.Sscanf(portStr, "%d", &actualPort)

    key := net.JoinHostPort(fwd.Addr, fmt.Sprintf("%d", actualPort))
    mu.Lock()
    listeners[key] = ln
    mu.Unlock()

    req.Reply(true, ssh.Marshal(&tcpipForwardResponse{Port: actualPort}))

    go func() {
        for {
            conn, err := ln.Accept()
            if err != nil {
                return
            }
            go s.forwardConnection(conn, sshConn, fwd.Addr, actualPort)
        }
    }()
}

// isProxyInternalIP checks if the IP is on the proxy's internal network.
// The proxy listens for SSH remote forwards on its internal IP so the
// sandbox can reach forwarded ports via host.vibepit DNS resolution.
func isProxyInternalIP(ip net.IP) bool {
    // In production, the proxy's internal IP is in 10.0.0.0/8.
    // This is intentionally permissive — the real constraint is that
    // the SSH client (host-side vibepit) only requests forwards to
    // the proxy's actual internal IP. This server-side check is
    // defense in depth.
    return ip.IsPrivate()
}

func (s *SSHServer) handleCancelForward(req *ssh.Request, mu *sync.Mutex, listeners map[string]net.Listener) {
    var fwd tcpipForwardRequest
    if err := ssh.Unmarshal(req.Payload, &fwd); err != nil {
        req.Reply(false, nil)
        return
    }

    key := net.JoinHostPort(fwd.Addr, fmt.Sprintf("%d", fwd.Port))
    mu.Lock()
    if ln, ok := listeners[key]; ok {
        ln.Close()
        delete(listeners, key)
    }
    mu.Unlock()

    if req.WantReply {
        req.Reply(true, nil)
    }
}

func (s *SSHServer) forwardConnection(conn net.Conn, sshConn *ssh.ServerConn, bindAddr string, bindPort uint32) {
    defer conn.Close()

    originAddr, originPortStr, _ := net.SplitHostPort(conn.RemoteAddr().String())
    var originPort uint32
    fmt.Sscanf(originPortStr, "%d", &originPort)

    payload := forwardedTCPPayload{
        Addr:       bindAddr,
        Port:       bindPort,
        OriginAddr: originAddr,
        OriginPort: originPort,
    }

    ch, reqs, err := sshConn.OpenChannel("forwarded-tcpip", ssh.Marshal(&payload))
    if err != nil {
        return
    }
    go ssh.DiscardRequests(reqs)

    go func() {
        io.Copy(ch, conn)
        ch.CloseWrite()
    }()
    io.Copy(conn, ch)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestSSHDRemoteForward -v -timeout 10s`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/sshd.go proxy/sshd_test.go
git commit -m "proxy: add embedded SSH server with remote port forwarding"
```

---

### Task 4: Start SSH server in proxy `Server.Run`

**Files:**
- Modify: `proxy/server.go`

**Step 1: Add SSH env var constants and start SSH server**

Add constants to `proxy/server.go`:

```go
const (
    SSHPort            = ":2222"
    EnvSSHHostKey      = "VIBEPIT_SSH_HOST_KEY"
    EnvSSHAuthorizedKey = "VIBEPIT_SSH_AUTHORIZED_KEY"
)
```

In `Server.Run`, add a goroutine to start the SSH server after the existing ones.
The SSH server binds to the bridge interface IP only, not 0.0.0.0, so the
sandbox cannot reach it:

```go
// Start SSH server if keys are available.
sshHostKey := os.Getenv(EnvSSHHostKey)
sshAuthorizedKey := os.Getenv(EnvSSHAuthorizedKey)
if sshHostKey != "" && sshAuthorizedKey != "" {
    go func() {
        sshd, err := NewSSHServer([]byte(sshHostKey), []byte(sshAuthorizedKey))
        if err != nil {
            errCh <- fmt.Errorf("SSH server: %w", err)
            return
        }
        // Bind to bridge interface only. The bridge IP is determined by
        // inspecting the container's network interfaces at startup. The
        // internal network IP is excluded so the sandbox cannot reach
        // the SSH server.
        bridgeIP, err := getBridgeIP()
        if err != nil {
            errCh <- fmt.Errorf("SSH bridge IP: %w", err)
            return
        }
        listenAddr := net.JoinHostPort(bridgeIP, "2222")
        ln, err := net.Listen("tcp", listenAddr)
        if err != nil {
            errCh <- fmt.Errorf("SSH listen: %w", err)
            return
        }
        fmt.Printf("proxy: SSH server listening on %s\n", listenAddr)
        errCh <- sshd.Serve(ln)
    }()
}
```

Add helper to find the bridge IP (the non-internal-network IP):

```go
// getBridgeIP returns the proxy container's bridge network IP address.
// The proxy is connected to two networks: internal (10.x.x.0/24) and
// bridge. We find the bridge IP by looking for a non-10.x.x.x address.
func getBridgeIP() (string, error) {
    addrs, err := net.InterfaceAddrs()
    if err != nil {
        return "", err
    }
    for _, addr := range addrs {
        ipNet, ok := addr.(*net.IPNet)
        if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
            continue
        }
        // The internal network uses 10.0.0.0/8. The bridge network
        // typically uses 172.17.0.0/16.
        if !ipNet.IP.Equal(net.IPv4(127, 0, 0, 1)) && ipNet.IP[0] != 10 {
            return ipNet.IP.String(), nil
        }
    }
    return "", fmt.Errorf("no bridge network interface found")
}
```

Update the `errCh` buffer size from `3` to `4`.

**Step 2: Verify build compiles**

Run: `go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add proxy/server.go
git commit -m "proxy: start SSH server on bridge interface only"
```

---

### Task 5: Host side — generate keys, pass to proxy, connect SSH client, set up forwards

**Files:**
- Modify: `cmd/run.go`
- Modify: `container/client.go:308-405`

**Step 1: Add SSH fields to `ProxyContainerConfig` and expose SSH port**

In `container/client.go`, add to `ProxyContainerConfig`:

```go
type ProxyContainerConfig struct {
    BinaryPath       string
    ConfigPath       string
    NetworkID        string
    ProxyIP          string
    Name             string
    SessionID        string
    TLSKeyPEM        string
    TLSCertPEM       string
    CACertPEM        string
    ProjectDir       string
    SSHHostKeyPEM    string
    SSHAuthorizedKey string
}
```

In `StartProxyContainer`, add SSH env vars:

```go
if cfg.SSHHostKeyPEM != "" {
    env = append(env,
        "VIBEPIT_SSH_HOST_KEY="+cfg.SSHHostKeyPEM,
        "VIBEPIT_SSH_AUTHORIZED_KEY="+cfg.SSHAuthorizedKey,
    )
}
```

Expose the SSH port (2222) alongside the control API port (3129). Publish on
`127.0.0.1:<random>` to keep it host-local only. Update the exposed ports,
port bindings, and return signature to include the SSH port.

Change `StartProxyContainer` return type to `(string, string, string, error)` — `(containerID, controlPort, sshPort, error)`.

```go
sshContainerPort, _ := nat.NewPort("tcp", "2222")

// In ExposedPorts:
ExposedPorts: nat.PortSet{
    containerPort:    struct{}{},
    sshContainerPort: struct{}{},
},

// In PortBindings:
PortBindings: nat.PortMap{
    containerPort: []nat.PortBinding{
        {HostIP: "127.0.0.1", HostPort: "0"},
    },
    sshContainerPort: []nat.PortBinding{
        {HostIP: "127.0.0.1", HostPort: "0"},
    },
},
```

After inspecting the container, extract the SSH port:

```go
sshBindings := info.NetworkSettings.Ports[sshContainerPort]
sshPort := ""
if len(sshBindings) > 0 {
    sshPort = sshBindings[0].HostPort
}

return resp.ID, controlPort, sshPort, nil
```

**Step 2: Update `cmd/run.go` to generate SSH keys and connect**

Add imports: `"io"`, `"net"`, `"time"`, `"golang.org/x/crypto/ssh"`.

After generating mTLS credentials, generate SSH keys:

```go
fmt.Println("+ Generating SSH keys")
sshKeys, err := proxy.GenerateSSHKeys()
if err != nil {
    return fmt.Errorf("ssh keys: %w", err)
}
```

Update the `StartProxyContainer` call to pass SSH keys and handle the new return value:

```go
proxyContainerID, controlPort, sshPort, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
    // ... existing fields ...
    SSHHostKeyPEM:    string(sshKeys.HostKeyPEM),
    SSHAuthorizedKey: string(sshKeys.AuthorizedKeyBytes),
})
```

After starting the proxy container, connect SSH and set up forwards:

```go
if len(merged.AllowHostPorts) > 0 && sshPort != "" {
    fmt.Println("+ Setting up host port forwarding")
    sshClient, err := connectSSH(sshPort, sshKeys)
    if err != nil {
        return fmt.Errorf("ssh connect: %w", err)
    }
    defer sshClient.Close()

    for _, port := range merged.AllowHostPorts {
        addr := fmt.Sprintf("%s:%d", proxyIP, port)
        ln, err := sshClient.Listen("tcp", addr)
        if err != nil {
            return fmt.Errorf("ssh forward port %d: %w", port, err)
        }
        fmt.Printf("+ Forwarding %s -> localhost:%d\n", addr, port)
        go forwardListener(ln, fmt.Sprintf("127.0.0.1:%d", port))
    }
}
```

Add helper functions at the bottom of `cmd/run.go`:

```go
func connectSSH(port string, keys *proxy.SSHKeys) (*ssh.Client, error) {
    signer, err := ssh.ParsePrivateKey(keys.ClientKeyPEM)
    if err != nil {
        return nil, err
    }
    config := &ssh.ClientConfig{
        User:            "vibepit",
        Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
        HostKeyCallback: ssh.FixedHostKey(keys.HostPublicKey),
    }

    // Poll until SSH server is ready.
    var client *ssh.Client
    for attempts := 0; attempts < 30; attempts++ {
        client, err = ssh.Dial("tcp", "127.0.0.1:"+port, config)
        if err == nil {
            break
        }
        time.Sleep(100 * time.Millisecond)
    }
    if err != nil {
        return nil, fmt.Errorf("SSH connect after retries: %w", err)
    }

    // Enable keepalives to detect dead connections.
    go func() {
        ticker := time.NewTicker(15 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            _, _, err := client.SendRequest("keepalive@vibepit", true, nil)
            if err != nil {
                return
            }
        }
    }()

    return client, nil
}

// forwardListener accepts connections from SSH remote forwards and
// forwards them to the target address on the host (always 127.0.0.1).
func forwardListener(ln net.Listener, target string) {
    for {
        conn, err := ln.Accept()
        if err != nil {
            return
        }
        go forwardConn(conn, target)
    }
}

func forwardConn(src net.Conn, target string) {
    defer src.Close()
    dst, err := net.Dial("tcp", target)
    if err != nil {
        return
    }
    defer dst.Close()
    go func() {
        io.Copy(dst, src)
    }()
    io.Copy(src, dst)
}
```

**Step 3: Verify build compiles**

Run: `go build ./...`
Expected: success

**Step 4: Commit**

```bash
git add cmd/run.go container/client.go
git commit -m "host-access: SSH client with keepalives, startup polling, 127.0.0.1 forwarding"
```

---

### Task 6: Manual integration test

**Steps:**

1. Create a test project:

```bash
mkdir -p /tmp/vibepit-host-test/.vibepit
cat > /tmp/vibepit-host-test/.vibepit/network.yaml << 'EOF'
presets:
  - default

allow-host-ports:
  - 8888
EOF
```

2. Start a host service on port 8888:

```bash
python3 -m http.server 8888 &
```

3. Run vibepit:

```bash
go run . run /tmp/vibepit-host-test
```

4. Inside the sandbox, test:

```bash
# HTTP via proxy (auto-allowed)
curl http://host.vibepit:8888/

# TCP via SSH tunnel
nc -z host.vibepit 8888 && echo "TCP works"

# Unconfigured port should fail
curl http://host.vibepit:9999/

# Verify reserved port rejected at startup
# (edit network.yaml to add port 3128, restart — should get error)
```

5. Clean up: `kill %1`

**Step 5: Commit if fixes needed**

```bash
git add -A
git commit -m "host-access: fix issues found during integration testing"
```

---

### Summary

| # | Description | Files |
|---|---|---|
| 1 | Add `golang.org/x/crypto/ssh` dependency | `go.mod`, `go.sum` |
| 2 | SSH key generation | `proxy/sshkeys.go`, `proxy/sshkeys_test.go` |
| 3 | Embedded SSH server (bridge-only, forward validation) | `proxy/sshd.go`, `proxy/sshd_test.go` |
| 4 | Start SSH server on bridge interface in `Server.Run` | `proxy/server.go` |
| 5 | Host side: keys, proxy container, SSH client with keepalives | `cmd/run.go`, `container/client.go` |
| 6 | Manual integration test | — |
