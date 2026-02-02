# Host Access Part 1: Config, DNS, and HTTP Proxy — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `host.vibepit` DNS resolution and HTTP proxy support so sandbox processes can reach host services via the proxy. This part does not include SSH tunneling — HTTP-only access.

**Architecture:** The DNS server returns the proxy IP for `host.vibepit` unconditionally. The HTTP proxy auto-allows `host.vibepit` requests for ports listed in `allow-host-ports` config, and rewrites the target to the actual host gateway IP. Unconfigured ports fall through to the normal allowlist.

**Tech Stack:** Existing `proxy`, `config`, `container`, `cmd` packages. No new dependencies.

---

### Task 1: Add `AllowHostPorts` to config

**Files:**
- Modify: `config/config.go:15-39`
- Modify: `config/config_test.go`

**Step 1: Write the failing test**

Add to `config/config_test.go`:

```go
t.Run("merges allow-host-ports from project config", func(t *testing.T) {
    dir := t.TempDir()
    projectDir := filepath.Join(dir, "project", ".vibepit")
    os.MkdirAll(projectDir, 0o755)

    projectFile := filepath.Join(projectDir, "network.yaml")
    os.WriteFile(projectFile, []byte(`
allow-host-ports:
  - 9200
  - 5432
`), 0o644)

    cfg, err := Load("/nonexistent/global.yaml", projectFile)
    if err != nil {
        t.Fatalf("Load() error: %v", err)
    }

    merged := cfg.Merge(nil, nil)

    if len(merged.AllowHostPorts) != 2 {
        t.Fatalf("expected 2 host ports, got %d", len(merged.AllowHostPorts))
    }
    if merged.AllowHostPorts[0] != 9200 || merged.AllowHostPorts[1] != 5432 {
        t.Errorf("unexpected ports: %v", merged.AllowHostPorts)
    }
})
```

**Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestLoadAndMerge/merges_allow-host-ports -v`
Expected: FAIL — `AllowHostPorts` field does not exist.

**Step 3: Write minimal implementation**

In `config/config.go`, add `AllowHostPorts` to `ProjectConfig`:

```go
type ProjectConfig struct {
    Presets        []string `koanf:"presets"`
    Allow          []string `koanf:"allow"`
    DNSOnly        []string `koanf:"dns-only"`
    AllowHTTP      bool     `koanf:"allow-http"`
    AllowHostPorts []int    `koanf:"allow-host-ports"`
}
```

Add `AllowHostPorts`, `ProxyIP`, and `HostGateway` to `MergedConfig`:

```go
type MergedConfig struct {
    Allow          []string `json:"allow"`
    DNSOnly        []string `json:"dns-only"`
    BlockCIDR      []string `json:"block-cidr"`
    AllowHTTP      bool     `json:"allow-http"`
    AllowHostPorts []int    `json:"allow-host-ports"`
    ProxyIP        string   `json:"proxy-ip,omitempty"`
    HostGateway    string   `json:"host-gateway,omitempty"`
}
```

`ProxyIP` and `HostGateway` are not set by `Merge()` — they will be set in `cmd/run.go` after network creation (Task 5). Add them now to avoid touching this struct twice.

In the `Merge` method, copy the field through:

```go
return MergedConfig{
    Allow:          allow,
    DNSOnly:        dnsOnly,
    BlockCIDR:      c.Global.BlockCIDR,
    AllowHTTP:      c.Global.AllowHTTP || c.Project.AllowHTTP,
    AllowHostPorts: c.Project.AllowHostPorts,
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./config/ -v`
Expected: all PASS

**Step 5: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "config: add allow-host-ports, proxy-ip, and host-gateway fields"
```

---

### Task 2: Add fields to `ProxyConfig`

**Files:**
- Modify: `proxy/server.go:20-27`

**Step 1: Add the fields to `ProxyConfig`**

In `proxy/server.go`:

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
}
```

Since `MergedConfig` is marshalled to JSON and becomes the proxy config file, these fields flow through automatically when set.

**Step 2: Verify the build compiles**

Run: `go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add proxy/server.go
git commit -m "proxy: add allow-host-ports, proxy-ip, host-gateway to ProxyConfig"
```

---

### Task 3: DNS — resolve `host.vibepit` to proxy IP

**Files:**
- Modify: `proxy/dns.go:14-30,32-86`
- Modify: `proxy/dns_test.go`

**Step 1: Write the failing test**

Add to `proxy/dns_test.go`:

```go
func TestDNSHostVibepit(t *testing.T) {
    al := NewAllowlist(nil)
    dnsOnly := NewAllowlist(nil)
    blocker := NewCIDRBlocker(nil)
    log := NewLogBuffer(100)

    proxyIP := net.ParseIP("10.42.0.2")
    srv := NewDNSServer(al, dnsOnly, blocker, log, "8.8.8.8:53")
    srv.SetProxyIP(proxyIP)
    addr, cleanup := srv.ListenAndServeTest()
    defer cleanup()

    time.Sleep(50 * time.Millisecond)

    c := new(dns.Client)

    t.Run("resolves host.vibepit to proxy IP", func(t *testing.T) {
        m := new(dns.Msg)
        m.SetQuestion("host.vibepit.", dns.TypeA)

        r, _, err := c.Exchange(m, addr)
        if err != nil {
            t.Fatalf("DNS exchange error: %v", err)
        }
        if r.Rcode != dns.RcodeSuccess {
            t.Fatalf("rcode = %d, want SUCCESS", r.Rcode)
        }
        if len(r.Answer) != 1 {
            t.Fatalf("expected 1 answer, got %d", len(r.Answer))
        }
        a, ok := r.Answer[0].(*dns.A)
        if !ok {
            t.Fatalf("expected A record, got %T", r.Answer[0])
        }
        if !a.A.Equal(proxyIP) {
            t.Errorf("resolved IP = %s, want %s", a.A, proxyIP)
        }
    })

    t.Run("host.vibepit is logged", func(t *testing.T) {
        entries := log.Entries()
        for _, e := range entries {
            if e.Domain == "host.vibepit" && e.Action == ActionAllow && e.Source == SourceDNS {
                return
            }
        }
        t.Error("expected log entry for host.vibepit DNS resolution")
    })
}
```

Add `"net"` to the imports in the test file.

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestDNSHostVibepit -v`
Expected: FAIL — `SetProxyIP` method does not exist.

**Step 3: Implement**

Add a `proxyIP` field and setter to `DNSServer` in `proxy/dns.go`:

```go
type DNSServer struct {
    allowlist *Allowlist
    dnsOnly   *Allowlist
    cidr      *CIDRBlocker
    log       *LogBuffer
    upstream  string
    proxyIP   net.IP
}

func (s *DNSServer) SetProxyIP(ip net.IP) {
    s.proxyIP = ip
}
```

In the `handler()` function, add a synthetic response before the allowlist check (after extracting the domain):

```go
domain := strings.TrimSuffix(strings.ToLower(r.Question[0].Name), ".")

// Synthetic response for host.vibepit — always resolves to proxy IP.
if domain == "host.vibepit" && s.proxyIP != nil && r.Question[0].Qtype == mdns.TypeA {
    s.log.Add(LogEntry{
        Time:   time.Now(),
        Domain: domain,
        Action: ActionAllow,
        Source: SourceDNS,
    })
    m := new(mdns.Msg)
    m.SetReply(r)
    m.Answer = append(m.Answer, &mdns.A{
        Hdr: mdns.RR_Header{
            Name:   r.Question[0].Name,
            Rrtype: mdns.TypeA,
            Class:  mdns.ClassINET,
            Ttl:    60,
        },
        A: s.proxyIP,
    })
    w.WriteMsg(m)
    return
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestDNSHostVibepit -v`
Expected: PASS

**Step 5: Run all proxy tests**

Run: `go test ./proxy/ -v`
Expected: all PASS

**Step 6: Commit**

```bash
git add proxy/dns.go proxy/dns_test.go
git commit -m "dns: resolve host.vibepit to proxy IP"
```

---

### Task 4: HTTP proxy — auto-allow and rewrite `host.vibepit`

**Files:**
- Modify: `proxy/http.go:15-28,30-67,69-131`
- Modify: `proxy/http_test.go`

**Step 1: Write the failing tests**

Add to `proxy/http_test.go` (add `"net"`, `"strconv"` to imports):

```go
func TestHTTPProxyHostVibepit(t *testing.T) {
    // Start a backend server simulating a host service.
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("host-service"))
    }))
    defer backend.Close()

    backendURL, _ := url.Parse(backend.URL)
    _, backendPort, _ := net.SplitHostPort(backendURL.Host)
    backendPortInt, _ := strconv.Atoi(backendPort)

    t.Run("auto-allows host.vibepit for configured port", func(t *testing.T) {
        al := NewAllowlist(nil)
        blocker := &CIDRBlocker{} // empty so localhost isn't blocked
        log := NewLogBuffer(100)
        p := NewHTTPProxy(al, blocker, log, true)
        p.SetHostVibepit(backendURL.Host, []int{backendPortInt})

        srv := httptest.NewServer(p.Handler())
        defer srv.Close()

        proxyURL, _ := url.Parse(srv.URL)
        client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

        resp, err := client.Get("http://host.vibepit:" + backendPort + "/")
        require.NoError(t, err)
        defer resp.Body.Close()

        assert.Equal(t, http.StatusOK, resp.StatusCode)
        body, _ := io.ReadAll(resp.Body)
        assert.Equal(t, "host-service", string(body))
    })

    t.Run("blocks host.vibepit for unconfigured port", func(t *testing.T) {
        al := NewAllowlist(nil)
        blocker := &CIDRBlocker{}
        log := NewLogBuffer(100)
        p := NewHTTPProxy(al, blocker, log, true)
        p.SetHostVibepit(backendURL.Host, []int{9999}) // different port

        srv := httptest.NewServer(p.Handler())
        defer srv.Close()

        proxyURL, _ := url.Parse(srv.URL)
        client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

        resp, err := client.Get("http://host.vibepit:" + backendPort + "/")
        require.NoError(t, err)
        defer resp.Body.Close()

        assert.Equal(t, http.StatusForbidden, resp.StatusCode)
    })

    t.Run("host.vibepit subject to allowlist when no host ports configured", func(t *testing.T) {
        al := NewAllowlist([]string{"host.vibepit:" + backendPort})
        blocker := &CIDRBlocker{}
        log := NewLogBuffer(100)
        p := NewHTTPProxy(al, blocker, log, true)
        p.SetHostVibepit(backendURL.Host, nil)

        srv := httptest.NewServer(p.Handler())
        defer srv.Close()

        proxyURL, _ := url.Parse(srv.URL)
        client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

        resp, err := client.Get("http://host.vibepit:" + backendPort + "/")
        require.NoError(t, err)
        defer resp.Body.Close()

        assert.Equal(t, http.StatusOK, resp.StatusCode)
    })
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestHTTPProxyHostVibepit -v`
Expected: FAIL — `SetHostVibepit` does not exist.

**Step 3: Implement**

Add fields and method to `HTTPProxy` in `proxy/http.go` (add `"strconv"` to imports):

```go
type HTTPProxy struct {
    allowlist      *Allowlist
    cidr           *CIDRBlocker
    log            *LogBuffer
    proxy          *goproxy.ProxyHttpServer
    hostGateway    string
    allowHostPorts map[int]bool
}

func (p *HTTPProxy) SetHostVibepit(hostGateway string, ports []int) {
    p.hostGateway = hostGateway
    p.allowHostPorts = make(map[int]bool)
    for _, port := range ports {
        p.allowHostPorts[port] = true
    }
}

func (p *HTTPProxy) isHostPortAllowed(port string) bool {
    if len(p.allowHostPorts) == 0 {
        return false
    }
    portNum, err := strconv.Atoi(port)
    if err != nil {
        return false
    }
    return p.allowHostPorts[portNum]
}
```

In the CONNECT handler, add before the existing allowlist check:

```go
hostname, port := splitHostPort(host, "443")

// host.vibepit: auto-allow if port is in allow-host-ports.
if hostname == "host.vibepit" && p.hostGateway != "" {
    if p.isHostPortAllowed(port) {
        p.log.Add(LogEntry{
            Time:   time.Now(),
            Domain: hostname,
            Port:   port,
            Action: ActionAllow,
            Source: SourceProxy,
            Reason: "allow-host-ports",
        })
        return goproxy.OkConnect, net.JoinHostPort(p.hostGateway, port)
    }
    // Fall through to normal allowlist check. If allowed, rewrite target.
}
```

And at the existing `return goproxy.OkConnect, host` line, add rewrite logic:

```go
// Rewrite host.vibepit to actual host gateway.
if hostname == "host.vibepit" && p.hostGateway != "" {
    return goproxy.OkConnect, net.JoinHostPort(p.hostGateway, port)
}
return goproxy.OkConnect, host
```

In the HTTP (DoFunc) handler, add before the `!allowHTTP` check:

```go
hostname, port := splitHostPort(req.Host, "80")

// host.vibepit: auto-allow if port is in allow-host-ports.
if hostname == "host.vibepit" && p.hostGateway != "" {
    if p.isHostPortAllowed(port) {
        p.log.Add(LogEntry{
            Time:   time.Now(),
            Domain: hostname,
            Port:   port,
            Action: ActionAllow,
            Source: SourceProxy,
            Reason: "allow-host-ports",
        })
        req.URL.Host = net.JoinHostPort(p.hostGateway, port)
        return req, nil
    }
    // Fall through to normal allowlist/HTTP checks.
    // If eventually allowed, still need to rewrite host.
}
```

And at the existing `return req, nil` (allow) line at the end, add rewrite:

```go
// Rewrite host.vibepit to actual host gateway.
if hostname == "host.vibepit" && p.hostGateway != "" {
    req.URL.Host = net.JoinHostPort(p.hostGateway, port)
}
return req, nil
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestHTTPProxyHostVibepit -v`
Expected: PASS

**Step 5: Run all proxy tests**

Run: `go test ./proxy/ -v`
Expected: all PASS

**Step 6: Commit**

```bash
git add proxy/http.go proxy/http_test.go
git commit -m "proxy: auto-allow host.vibepit for configured ports, rewrite to host gateway"
```

---

### Task 5: Wire up DNS and HTTP in `Server.Run`, set ProxyIP/HostGateway in `cmd/run.go`

**Files:**
- Modify: `proxy/server.go:52-95`
- Modify: `cmd/run.go:154-174,222-234`
- Modify: `container/client.go:327-405`

**Step 1: Update `Server.Run` to configure DNS and HTTP proxy**

In `proxy/server.go`, in the `Run` method, after creating the DNS server and HTTP proxy:

```go
httpProxy := NewHTTPProxy(allowlist, cidr, log, s.config.AllowHTTP)
dnsServer := NewDNSServer(allowlist, dnsOnlyList, cidr, log, s.config.Upstream)
controlAPI := NewControlAPI(log, s.config, allowlist)

// Configure host.vibepit support.
if s.config.ProxyIP != "" {
    proxyIP := net.ParseIP(s.config.ProxyIP)
    if proxyIP != nil {
        dnsServer.SetProxyIP(proxyIP)
    }
}
if s.config.HostGateway != "" {
    httpProxy.SetHostVibepit(s.config.HostGateway, s.config.AllowHostPorts)
}
```

Add `"net"` to the imports in `proxy/server.go`.

**Step 2: Set `ProxyIP` and `HostGateway` in `cmd/run.go`**

After `merged := cfg.Merge(...)` and after `proxyIP` is known (after `CreateNetwork`), set:

```go
merged.ProxyIP = proxyIP
merged.HostGateway = "host-gateway"
```

Move the JSON marshalling of `merged` to after network creation (it's currently at line 167, before network creation at line 194). The proxy config JSON must include the proxy IP and host gateway.

Move these lines (currently at 167-174):

```go
proxyConfig, _ := json.Marshal(merged)
tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
if err != nil {
    return err
}
defer os.Remove(tmpFile.Name())
tmpFile.Write(proxyConfig)
tmpFile.Close()
```

To after line 198 (after `proxyIP := netInfo.ProxyIP`), and add the new fields before marshalling:

```go
proxyIP := netInfo.ProxyIP

merged.ProxyIP = proxyIP
merged.HostGateway = "host-gateway"

proxyConfig, _ := json.Marshal(merged)
tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
if err != nil {
    return err
}
defer os.Remove(tmpFile.Name())
tmpFile.Write(proxyConfig)
tmpFile.Close()
```

**Step 3: Add `host-gateway` extra host to proxy container**

In `container/client.go`, in `StartProxyContainer`, add `ExtraHosts` to the `HostConfig`:

```go
&container.HostConfig{
    Binds: []string{
        cfg.BinaryPath + ":" + ProxyBinaryPath + ":ro",
        cfg.ConfigPath + ":" + ProxyConfigPath + ":ro",
    },
    RestartPolicy: container.RestartPolicy{Name: "no"},
    ExtraHosts:    []string{"host-gateway:host-gateway"},
    PortBindings: nat.PortMap{
        containerPort: []nat.PortBinding{
            {HostIP: "127.0.0.1", HostPort: "0"},
        },
    },
},
```

Docker resolves the special value `host-gateway` to the host's IP address.

**Step 4: Verify build compiles**

Run: `go build ./...`
Expected: success

**Step 5: Commit**

```bash
git add proxy/server.go cmd/run.go container/client.go
git commit -m "host-access: wire up DNS proxyIP, HTTP host gateway, and host-gateway extra host"
```

---

### Summary

| # | Description | Files |
|---|---|---|
| 1 | Add `AllowHostPorts` to config | `config/config.go`, `config/config_test.go` |
| 2 | Add fields to `ProxyConfig` | `proxy/server.go` |
| 3 | DNS: resolve `host.vibepit` to proxy IP | `proxy/dns.go`, `proxy/dns_test.go` |
| 4 | HTTP proxy: auto-allow + rewrite | `proxy/http.go`, `proxy/http_test.go` |
| 5 | Wire up in `Server.Run` and `cmd/run.go` | `proxy/server.go`, `cmd/run.go`, `container/client.go` |

After this plan is complete, Part 2 (SSH server, key generation, remote port forwarding) can be implemented to add non-HTTP TCP support.
