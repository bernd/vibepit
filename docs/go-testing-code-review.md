# Go Testing Code Review

## Checklist Summary

| Criterion | Status | Notes |
|-----------|--------|-------|
| Table-driven with clear case names | Pass (mostly) | Good use throughout, a few files miss opportunities |
| Subtests use `t.Run` | Pass (mostly) | Some table tests miss `t.Run` |
| Names describe behavior | Pass | Clear, descriptive names |
| Errors include got/want | Pass (mostly) | A few bare assertions lack context messages |
| Cleanup via `t.Cleanup` | Pass | Good use of `t.TempDir()`, `t.Cleanup`, `t.Setenv` |
| Parallel tests isolated | N/A | No `t.Parallel` used; no shared mutable state issues |
| Mocks use test-local interfaces | Pass | `stubScreen`, `switchScreen` defined in test files |
| Edge cases and error paths | Pass | Good coverage of boundary conditions |

## Issues

### 1. `container/client_test.go` — Tests verify struct fields, not behavior

**Severity: Medium**

`TestDevContainerConfigEnvBuild` and `TestProxyContainerConfig` construct a struct and check its fields are what was assigned. These test Go's assignment semantics, not application behavior.

```go
// current (lines 27-66)
cfg := DevContainerConfig{Image: "vibepit:latest", ...}
if cfg.Image != "vibepit:latest" {  // always true
    t.Error("unexpected image")
}
```

Either remove them or replace them with tests that exercise actual logic (e.g., how the config is used to build container arguments).

### 2. `container/client_test.go:19` — Table-driven test missing `t.Run`

**Severity: Low**

`TestNextIP` uses a table but doesn't wrap iterations in `t.Run`, so failures don't identify which case failed.

```go
// current
for _, tt := range tests {
    got := nextIP(net.ParseIP(tt.input))
    if got.String() != tt.expected {
        t.Errorf(...)
    }
}

// suggested
for _, tt := range tests {
    t.Run(tt.input, func(t *testing.T) {
        got := nextIP(net.ParseIP(tt.input))
        assert.Equal(t, tt.expected, got.String())
    })
}
```

### 3. `tui/cursor_test.go` — Repetitive individual tests instead of table-driven

**Severity: Low**

There are 9 separate `TestCursor_HandleKey_*` functions that follow the same pattern: create a `Cursor`, call `HandleKey`, assert position. These should be a single table-driven test with fields `{name, initial Cursor, key, wantPos, wantHandled}`.

### 4. `proxy/dns_test.go:21` — `time.Sleep` for server readiness

**Severity: Medium**

```go
time.Sleep(50 * time.Millisecond)
```

This is a race condition waiting to happen in CI. The test should either use a ready channel/callback from `ListenAndServeTest` or retry the first exchange with a short backoff.

### 5. `integration_test.go:59` — `time.Sleep` for server readiness

**Severity: Medium**

Same issue as above, with a larger sleep:

```go
time.Sleep(500 * time.Millisecond)
```

### 6. `integration_test.go:46-48` — Cleanup via `defer os.Remove` instead of `t.Cleanup`

**Severity: Low**

```go
tmpFile, _ := os.CreateTemp("", "proxy-test-*.json")
tmpFile.Write(data)
tmpFile.Close()
defer os.Remove(tmpFile.Name())
```

Should use `t.Cleanup` for consistency with the rest of the codebase. Also, the errors from `CreateTemp` and `Write` are silently discarded — use `require.NoError`.

### 7. `proxy/api_test.go` — Mixed assertion styles within single test function

**Severity: Low**

The first four subtests use raw `if/t.Errorf` while the last three use `testify/assert`. Within a single test function, pick one style. Since the project uses testify everywhere else, the first subtests should use `assert`/`require` too.

```go
// lines 32-42: raw style
if w.Code != http.StatusOK {
    t.Fatalf("status = %d, want 200", w.Code)
}

// lines 89-93: testify style
require.Equal(t, http.StatusOK, w.Code)
```

### 8. `proxy/log_test.go` — Mixed assertion styles

**Severity: Low**

Same issue. The first two subtests use raw `if/t.Errorf`, while "assigns sequential IDs" uses `assert`/`require`. The `stats` subtest goes back to raw style.

### 9. `config/config_test.go` — Helper `contains()` duplicates `slices.Contains`

**Severity: Low**

Lines 157-164 define a custom `contains` helper. Since Go 1.21, `slices.Contains` is available in the standard library, making this redundant.

### 10. `proxy/http_test.go`, `proxy/dns_test.go` — Repeated log-scanning pattern

**Severity: Low**

The pattern of scanning log entries to find a match appears in both files:

```go
var found bool
for _, e := range entries {
    if e.Domain == "evil.com" && e.Action == ActionBlock {
        found = true
        break
    }
}
assert.True(t, found, "blocked request not found in log")
```

A small test helper like `findLogEntry(entries, domain, action)` would reduce duplication.

### 11. Assertions missing context messages in multiple files

**Severity: Low**

Many `assert.Equal` / `assert.True` calls lack a context message, making failures harder to diagnose:

- `tui/cursor_test.go` — all assertions lack messages
- `tui/screen_test.go:43` — `assert.NotNil(t, cmd)` — which cmd?
- `cmd/monitor_ui_test.go:13-14` — `assert.True(t, handled)` — handled what?

When tests are inside `t.Run` subtests with good names this is less critical, but standalone test functions benefit from context strings.

## What's Done Well

- **Allowlist tests** (`proxy/allowlist_test.go`) — excellent table-driven structure with clear case names covering exact match, wildcards, port-specific rules, and edge cases.
- **mTLS tests** (`proxy/mtls_test.go`) — thorough coverage of certificate generation, PEM round-tripping, full handshake verification including negative cases (no cert, wrong CA).
- **Log buffer tests** (`proxy/log_test.go`) — good coverage of ring buffer wrap-around behavior and cursor-based pagination.
- **Setup UI tests** (`config/setup_ui_test.go`) — comprehensive coverage of expand/collapse, toggle, navigation, and implicit inclusion logic.
- **Test isolation** — consistent use of `t.TempDir()` and `t.Setenv()` throughout. No shared mutable package-level state.
- **Session credential tests** (`cmd/session_test.go`) — clean write/read/cleanup lifecycle test with proper permission verification.
