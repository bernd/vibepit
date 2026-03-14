# Random Proxy Ports Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace hardcoded proxy ports (3128, 3129, 2222) with random ports from the ephemeral range (49152-65535) so users can freely use any port in `allow-host-ports` without conflicts.

**Architecture:** The host-side `cmd/run.go` generates random ports before starting the proxy container, passes them via the existing JSON config, and the proxy binds to those ports instead of constants. The dev container already receives `HTTP_PROXY` dynamically. DNS port 53 stays fixed (OS resolvers require it).

**Tech Stack:** Go, Docker API, existing config JSON pipeline

---

### Task 1: Add port fields to config structs and generate random ports

**Files:**
- Modify: `config/config.go:38-66`
- Test: `config/config_test.go:97-111`

**Step 1: Write the failing test**

In `config/config_test.go`, replace the "rejects reserved proxy ports" test with a new test for `RandomProxyPort`:

```go
t.Run("generates random port in ephemeral range avoiding excluded", func(t *testing.T) {
	excluded := map[int]bool{55000: true, 55001: true}
	for i := 0; i < 100; i++ {
		port, err := RandomProxyPort(excluded)
		if err != nil {
			t.Fatalf("RandomProxyPort() error: %v", err)
		}
		if port < 49152 || port > 65535 {
			t.Errorf("port %d outside ephemeral range", port)
		}
		if excluded[port] {
			t.Errorf("port %d is in excluded set", port)
		}
	}
})
```

**Step 2: Run test to verify it fails**

Run: `go test ./config/ -run "generates random port" -v`
Expected: FAIL — `RandomProxyPort` undefined

**Step 3: Write minimal implementation**

In `config/config.go`:

1. Add `ProxyPort` and `ControlAPIPort` fields to `MergedConfig`:

```go
type MergedConfig struct {
	Allow          []string `json:"allow"`
	DNSOnly        []string `json:"dns-only"`
	BlockCIDR      []string `json:"block-cidr"`
	AllowHTTP      bool     `json:"allow-http"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip,omitempty"`
	HostGateway    string   `json:"host-gateway,omitempty"`
	ProxyPort      int      `json:"proxy-port,omitempty"`
	ControlAPIPort int      `json:"control-api-port,omitempty"`
}
```

2. Replace `reservedProxyPorts` and `ValidateHostPorts` with `RandomProxyPort`:

```go
// RandomProxyPort returns a random port in the ephemeral range (49152-65535)
// that is not in the excluded set.
func RandomProxyPort(excluded map[int]bool) (int, error) {
	const lo, hi = 49152, 65535
	rangeSize := hi - lo + 1
	for i := 0; i < 100; i++ {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		port := lo + int(binary.BigEndian.Uint16(b[:]))%rangeSize
		if !excluded[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("failed to find available port after 100 attempts")
}
```

Add `"crypto/rand"` and `"encoding/binary"` to imports. Remove the `reservedProxyPorts` map and `ValidateHostPorts` method entirely.

**Step 4: Run test to verify it passes**

Run: `go test ./config/ -run "generates random port" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "config: add RandomProxyPort, remove reserved port validation"
```

---

### Task 2: Proxy reads ports from config instead of constants

**Files:**
- Modify: `proxy/server.go:13-19, 21-31, 56-99`

**Step 1: Write the failing test**

In a new section of an existing test file or inline — the proxy server is hard to unit-test since it starts listeners. Instead, verify the config struct accepts the new fields. Add to `proxy/api_test.go` (which already creates `ProxyConfig`):

```go
func TestProxyConfigPorts(t *testing.T) {
	cfg := ProxyConfig{
		ProxyPort:      51234,
		ControlAPIPort: 51235,
	}
	if cfg.ProxyPort != 51234 {
		t.Errorf("expected proxy port 51234, got %d", cfg.ProxyPort)
	}
	if cfg.ControlAPIPort != 51235 {
		t.Errorf("expected control API port 51235, got %d", cfg.ControlAPIPort)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestProxyConfigPorts -v`
Expected: FAIL — `ProxyConfig` has no field `ProxyPort`

**Step 3: Write minimal implementation**

In `proxy/server.go`:

1. Remove `ProxyPort` and `ControlAPIPort` constants (keep `DefaultUpstreamDNS`, `DNSPort`, `LogBufferCapacity`):

```go
const (
	DefaultUpstreamDNS = "8.8.8.8:53"
	DNSPort            = ":53"
	LogBufferCapacity  = 10000
)
```

2. Add fields to `ProxyConfig`:

```go
type ProxyConfig struct {
	Allow          []string `json:"allow"`
	DNSOnly        []string `json:"dns-only"`
	BlockCIDR      []string `json:"block-cidr"`
	Upstream       string   `json:"upstream"`
	AllowHTTP      bool     `json:"allow-http"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip"`
	HostGateway    string   `json:"host-gateway"`
	ProxyPort      int      `json:"proxy-port"`
	ControlAPIPort int      `json:"control-api-port"`
}
```

3. Update `Run()` to use config ports:

```go
proxyAddr := fmt.Sprintf(":%d", s.config.ProxyPort)
controlAddr := fmt.Sprintf(":%d", s.config.ControlAPIPort)

go func() {
	fmt.Printf("proxy: HTTP proxy listening on %s\n", proxyAddr)
	errCh <- http.ListenAndServe(proxyAddr, httpProxy.Handler())
}()

// ... DNS stays on DNSPort ...

go func() {
	tlsCfg, err := LoadServerTLSConfigFromEnv()
	if err != nil {
		errCh <- fmt.Errorf("control API TLS: %w", err)
		return
	}
	fmt.Printf("proxy: control API listening on %s (mTLS)\n", controlAddr)
	ln, err := tls.Listen("tcp", controlAddr, tlsCfg)
	if err != nil {
		errCh <- err
		return
	}
	errCh <- http.Serve(ln, controlAPI)
}()
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestProxyConfigPorts -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/server.go proxy/api_test.go
git commit -m "proxy: read HTTP proxy and control API ports from config"
```

---

### Task 3: Wire random ports through cmd/run.go

**Files:**
- Modify: `cmd/run.go:158-162, 196-208, 264-276`

**Step 1: Update run.go to generate and pass random ports**

This is wiring code with no direct unit test (integration-tested by running the app). Changes:

1. After `merged := cfg.Merge(...)` (line 158), remove the `ValidateHostPorts` call (lines 160-162).

2. After setting `merged.ProxyIP` and `merged.HostGateway` (lines 198-199), generate random ports:

```go
merged.ProxyIP = proxyIP
merged.HostGateway = "host-gateway"

// Generate random ports for proxy services, avoiding user's host ports.
excluded := make(map[int]bool)
for _, p := range merged.AllowHostPorts {
	excluded[p] = true
}
proxyPort, err := config.RandomProxyPort(excluded)
if err != nil {
	return fmt.Errorf("proxy port: %w", err)
}
excluded[proxyPort] = true
controlAPIPort, err := config.RandomProxyPort(excluded)
if err != nil {
	return fmt.Errorf("control API port: %w", err)
}
merged.ProxyPort = proxyPort
merged.ControlAPIPort = controlAPIPort
```

3. Add `ProxyPort` to `DevContainerConfig` and pass it (see Task 4).

**Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: SUCCESS (may fail until Task 4 is done — that's fine, Tasks 3+4 are paired)

**Step 3: Commit**

```bash
git add cmd/run.go
git commit -m "cmd/run: generate random proxy ports, remove reserved port validation"
```

---

### Task 4: Pass dynamic proxy port to dev container

**Files:**
- Modify: `container/client.go:24-43, 464-493`

**Step 1: Update DevContainerConfig and StartDevContainer**

1. Remove the `ProxyPortNum` constant (line 42).

2. Add `ProxyPort` field to `DevContainerConfig`:

```go
type DevContainerConfig struct {
	Image      string
	ProjectDir string
	WorkDir    string
	VolumeName string
	NetworkID  string
	ProxyIP    string
	ProxyPort  int
	Name       string
	Term       string
	ColorTerm  string
	UID        int
	User       string
}
```

3. Use `cfg.ProxyPort` in `StartDevContainer` env vars:

```go
proxyPort := strconv.Itoa(cfg.ProxyPort)
env := []string{
	fmt.Sprintf("TERM=%s", cfg.Term),
	"LANG=en_US.UTF-8",
	"LC_ALL=en_US.UTF-8",
	fmt.Sprintf("VIBEPIT_PROJECT_DIR=%s", cfg.ProjectDir),
	fmt.Sprintf("HTTP_PROXY=http://%s:%s", cfg.ProxyIP, proxyPort),
	fmt.Sprintf("HTTPS_PROXY=http://%s:%s", cfg.ProxyIP, proxyPort),
	fmt.Sprintf("http_proxy=http://%s:%s", cfg.ProxyIP, proxyPort),
	fmt.Sprintf("https_proxy=http://%s:%s", cfg.ProxyIP, proxyPort),
	"NO_PROXY=localhost,127.0.0.1",
	"no_proxy=localhost,127.0.0.1",
}
```

Add `"strconv"` to imports.

**Step 2: Update cmd/run.go to pass ProxyPort**

In `cmd/run.go`, update the `StartDevContainer` call to include `ProxyPort`:

```go
devContainerID, err := client.StartDevContainer(ctx, ctr.DevContainerConfig{
	// ... existing fields ...
	ProxyPort:  proxyPort,
	// ...
})
```

**Step 3: Run build to verify compilation**

Run: `go build ./...`
Expected: SUCCESS

**Step 4: Commit**

```bash
git add container/client.go cmd/run.go
git commit -m "container: pass dynamic proxy port to dev container env vars"
```

---

### Task 5: Pass dynamic control API port to proxy container

**Files:**
- Modify: `container/client.go:321-419, 430-462`

**Step 1: Update ProxyContainerConfig and StartProxyContainer**

1. Add `ControlAPIPort` field to `ProxyContainerConfig`:

```go
type ProxyContainerConfig struct {
	BinaryPath     string
	ConfigPath     string
	NetworkID      string
	ProxyIP        string
	ControlAPIPort int
	Name           string
	SessionID      string
	TLSKeyPEM      string
	TLSCertPEM     string
	CACertPEM      string
	ProjectDir     string
}
```

2. In `StartProxyContainer`, use `cfg.ControlAPIPort` instead of hardcoded "3129":

```go
portStr := strconv.Itoa(cfg.ControlAPIPort)
containerPort, _ := nat.NewPort("tcp", portStr)
```

Add `"strconv"` to imports.

3. In `ListProxySessions`, replace `p.PrivatePort == 3129` with a label-based lookup. Add a new label constant and store the control API port as a container label:

```go
const LabelControlPort = "vibepit.control-port"
```

In `StartProxyContainer`, add the label:

```go
labels[LabelControlPort] = portStr
```

In `ListProxySessions`, read from label instead of scanning ports:

```go
for _, ctr := range containers {
	controlPort := ctr.Labels[LabelControlPort]
	// If label is missing (old containers), fall back to port scan.
	if controlPort == "" {
		for _, p := range ctr.Ports {
			if p.PublicPort != 0 {
				controlPort = fmt.Sprintf("%d", p.PublicPort)
				break
			}
		}
	}
	sessions = append(sessions, ProxySession{
		// ...
	})
}
```

**Step 2: Update cmd/run.go to pass ControlAPIPort**

```go
proxyContainerID, controlPort, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
	// ... existing fields ...
	ControlAPIPort: controlAPIPort,
	// ...
})
```

**Step 3: Run build to verify compilation**

Run: `go build ./...`
Expected: SUCCESS

**Step 4: Commit**

```bash
git add container/client.go cmd/run.go
git commit -m "container: pass dynamic control API port to proxy container"
```

---

### Task 6: Update host-access design doc

**Files:**
- Modify: `docs/plans/2026-02-02-host-access-design.md:88-89`

**Step 1: Update the validation paragraph**

Replace:
> **Validation:** Ports that conflict with proxy services (53, 3128, 3129, and the SSH server port) are rejected at config load time with a clear error message.

With:
> **Validation:** Port 53 is reserved for DNS. All other proxy services (HTTP proxy, control API, SSH) use random ports from the ephemeral range (49152-65535) to avoid conflicts with user-configured host ports.

**Step 2: Commit**

```bash
git add docs/plans/2026-02-02-host-access-design.md
git commit -m "docs: update host-access design for random proxy ports"
```

---

### Task 7: Run full test suite and verify

**Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All PASS

**Step 2: Build the binary**

Run: `go build -o vibepit .`
Expected: SUCCESS

**Step 3: Manual smoke test (optional)**

Run `./vibepit run` in a project directory and verify:
- Proxy starts on a high-numbered random port
- `HTTP_PROXY` env var inside the dev container uses the random port
- `allow-host-ports` with ports like 3128, 3129, 2222 works without errors

---

## Verification

1. `go test ./config/ -v` — `RandomProxyPort` tests pass, old reserved port tests removed
2. `go test ./proxy/ -v` — `ProxyConfig` accepts port fields, all existing tests pass
3. `go build ./...` — compiles cleanly
4. Manual: run vibepit, confirm random ports in proxy startup output and `HTTP_PROXY` env
