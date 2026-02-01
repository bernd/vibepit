# Go Code Review: vibepit

Reviewed: 38 source files across `cmd/`, `config/`, `container/`, `proxy/`, `tui/`, and `main.go`.

## Findings

### 1. Unchecked error from `tmpFile.Write` — `cmd/run.go:173`

```go
tmpFile.Write(proxyConfig)
```

The return value from `Write` is ignored. If the write fails (disk full, etc.), the proxy starts with a truncated or empty config, silently breaking network filtering — a security-sensitive path.

**Severity:** Medium (security-adjacent — proxy config integrity)

---

### 2. Unchecked error from `client.RemoveVolume` — `cmd/run.go:161`

```go
client.RemoveVolume(ctx, volumeName)
```

When `--clean` is specified, volume removal errors are silently ignored. If the volume isn't actually removed, the user gets stale state while believing they have a clean slate.

**Severity:** Low (user expectation mismatch, not a correctness bug)

---

### 3. Variable shadows package-level const — `cmd/run.go:156`

```go
volumeName := volumeName
```

The local variable `volumeName` shadows the package-level const of the same name. This works but is confusing. The shadowing seems intentional (perhaps anticipating per-project volumes) but currently serves no purpose since the value is never modified.

**Severity:** Low (readability)

---

### 4. `randomHex` is not actually random — `cmd/run.go:277-279`

```go
func randomHex() string {
    return fmt.Sprintf("%x%x%x", os.Getpid(), os.Getuid(), os.Getppid())
}
```

Despite the name, this function is deterministic (PID + UID + PPID). If a user stops and restarts vibepit within the same parent shell, the same container/network names will be generated. This can cause container name conflicts. Contrast with `randomSessionID` directly below it which correctly uses `crypto/rand`.

**Severity:** Medium (container name collisions)

---

### 5. LogBuffer ring buffer `full` flag logic — `proxy/log.go:64`

```go
b.pos = (b.pos + 1) % b.cap
if b.pos == 0 && !b.full {
    b.full = true
}
```

The `full` flag is only set when `pos` wraps to 0. This is correct for a ring buffer, but the `!b.full` condition is redundant — once `full` is true, `pos` will keep cycling and the condition will never be re-evaluated differently. Not a bug, just unnecessary.

**Severity:** None (informational)

---

### 6. DNS client created per-query without timeout — `proxy/dns.go:56-57`

```go
c := new(mdns.Client)
resp, _, err := c.Exchange(r, s.upstream)
```

A new DNS client is allocated per query with default settings. The default `miekg/dns` client has a 2-second timeout, which is reasonable, but creating a client per query is wasteful. More importantly, there's no retry logic — if the upstream is temporarily unreachable, every query fails.

**Severity:** Low (performance, resilience)

---

### 7. `ControlAPI.handleLogs` silently ignores parse error — `proxy/api.go:38`

```go
afterID, _ = strconv.ParseUint(s, 10, 64)
```

If a caller sends `?after=garbage`, the error is silently swallowed and `afterID` defaults to 0, returning the last 25 entries. This is arguably fine for an internal API but could mask debugging issues.

**Severity:** None (informational — internal API)

---

### 8. `writeJSON` doesn't check encode error — `proxy/api.go:69`

```go
func writeJSON(w http.ResponseWriter, v any) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(v)
}
```

If `Encode` fails, the client gets a partial JSON response with a 200 status. Since this is an internal mTLS-protected API with well-typed data, encoding failure is extremely unlikely.

**Severity:** None (informational — internal API, encoding failure near-impossible)

---

### 9. Goroutine leak potential in `ListenAndServe` — `proxy/dns.go:109-113`

```go
errCh := make(chan error, 2)
go func() { errCh <- udpServer.ListenAndServe() }()
go func() { errCh <- tcpServer.ListenAndServe() }()
return <-errCh
```

When one server fails, the function returns but the other goroutine keeps running with no shutdown mechanism. The `Server` struct has no `ctx` for cancellation. Since `Server.Run` also has the same pattern (returns on first error from errCh) and the process is expected to exit entirely when any component fails, this is acceptable in the current architecture.

**Severity:** Low (acceptable given the process lifecycle — proxy exits on any component failure)

---

### 10. `Server.Run` lacks graceful shutdown — `proxy/server.go:62-95`

All three servers (`http.ListenAndServe`, DNS, TLS control API) are started as fire-and-forget goroutines. When the context is canceled, the function returns but none of the listeners are closed. The proxy runs in a dedicated container that gets `StopAndRemove`'d, so this is acceptable but not ideal.

**Severity:** Low (mitigated by container lifecycle)

---

### 11. `resolveAndCheckCIDR` uses system resolver — `proxy/http.go:151`

```go
ips, err := net.LookupIP(hostname)
```

The HTTP proxy resolves hostnames using the system DNS, not through the filtering DNS server. This means the CIDR check in the HTTP proxy could see different IPs than what the DNS server returned to the container, creating a TOCTOU gap. An attacker controlling a DNS record could return a safe IP for the proxy's check but a private IP for the container's actual connection (though the CONNECT tunnel means the container connects directly).

**Severity:** Low (the DNS server already blocks private IPs, and the CONNECT handler checks before tunneling, so this is defense-in-depth with a minor inconsistency)

---

## Summary

| # | File | Issue | Severity |
|---|------|-------|----------|
| 1 | `cmd/run.go:173` | Unchecked `Write` error on proxy config | Medium |
| 2 | `cmd/run.go:161` | Unchecked `RemoveVolume` error | Low |
| 3 | `cmd/run.go:156` | Variable shadows package-level const | Low |
| 4 | `cmd/run.go:277` | `randomHex` is deterministic, not random | Medium |
| 5 | `proxy/log.go:64` | Redundant `!b.full` check | Info |
| 6 | `proxy/dns.go:56` | DNS client per-query, no retry | Low |
| 7 | `proxy/api.go:38` | Silent parse error on query param | Info |
| 8 | `proxy/api.go:69` | `writeJSON` ignores encode error | Info |
| 9 | `proxy/dns.go:109` | Surviving goroutine on listener failure | Low |
| 10 | `proxy/server.go:62` | No graceful shutdown of listeners | Low |
| 11 | `proxy/http.go:151` | TOCTOU between proxy DNS and container DNS | Low |

## Overall Assessment

The codebase is well-structured and idiomatic Go. Error handling is thorough throughout, with consistent `fmt.Errorf("context: %w", err)` wrapping. The concurrency model is appropriate for the container-per-session architecture — goroutine lifecycle is tied to process lifecycle via container teardown. The mTLS implementation follows best practices (TLS 1.3, ephemeral CA, separate server/client certs, CA key discarded after signing).

The two medium findings are worth addressing: the unchecked write on a security-critical config file (#1) and the misleadingly-named deterministic ID generator (#4).
