# TLS inspection, request rules, and secret injection

## Intent

Today the proxy tunnels HTTPS with `CONNECT` and decides per `host:port`.
The user wants two things that need the proxy to see inside HTTPS:

1. **Finer filtering:** allow or deny by HTTP method and path (for example,
   read-only access to five GitHub repos), not just whole hosts.
2. **Secret injection:** the agent uses tokens (for example a GitHub token)
   without the real value ever entering the sandbox.

A third goal came out of the discussion: an **audit mode** that shows every
request a tool makes, so presets can be authored from observed traffic.

Prior art reviewed: NVIDIA OpenShell, Docker Sandboxes (sbx), Vercel Sandbox,
iron-proxy, Anthropic sandbox-runtime, Coder boundary/httpjail, Cloudflare
outbound Workers, Fly tokenizer, Codex cloud. The design borrows OpenShell's
audit mode, credential/endpoint binding and placeholder model, Docker's
"same allow/deny verbs with optional method/path", and Vercel's "terminate
TLS only where rules need it".

## Goals

- Request-level allow and deny rules (method, path, optional CEL/expr
  condition) on selected hosts.
- Secret injection: tokens live on the host and in proxy memory only; the
  sandbox sees a placeholder.
- Session-wide audit mode plus a monitor action that drafts rules from
  observed requests.
- Named sets and parameterized presets so a list of repos is written once,
  including built-in `github-read` and `github-write` presets.
- Existing configs behave exactly as today.

## Non-goals (first version)

- Request-level holds (path refusals are immediate 403s).
- GraphQL matching beyond "query only" (operation names, fields), and MCP or
  other protocol-aware matching.
- Automatic secret refresh (manual `vibepit secret refresh` only; a future
  host daemon will take this over). Proxy-side OAuth/GitHub App refresh.
- `bool` preset inputs and conditional (`if:`) rules.
- Presets defined in project config.
- Per-rule or per-preset audit mode.
- Native keychain integration (the `command` source covers it).
- Placeholder replacement inside request bodies.
- Secret scanners, detection of real secret values in requests, body
  inspection, and a scan-wide termination switch. The inspector interface is
  built now; see section 6.
- Changing the `default` preset (it keeps `vcs-github`).
- Chaining through an upstream (corporate) proxy. The current proxy doesn't
  either; with the new dialer it would be a dialer option.
- Telemetry exporter configuration in vibepit. `netproxy` provides the
  telemetry APIs now; vibepit keeps exporting off.
- OAuth flows run by agents (Claude Code `/login`, Codex login). Planned with
  the host daemon; see "Future: OAuth with the host daemon". Host CLIs that
  manage their own OAuth (`gh auth token`, `gcloud auth print-access-token`)
  already work through `command` sources.

## Anti-goals

- Turning on the feature breaks a working setup. Hosts without request rules
  must stay tunneled.
- A secret reaches any host or path outside its service binding.
- An older vibepit binary silently ignores new deny rules (fail open).
- vibepit storing secret values at rest (disk, container config).
- Audit mode turning into "open internet with logging".

## Constraints

- `CGO_ENABLED=0`; the proxy runs on a distroless image; the macOS build
  embeds the Linux arm64 proxy binary, so binary size matters.
- The sandbox has a read-only root filesystem with `/tmp` as tmpfs.
- `netproxy` is built on the standard library (`net/http`, `crypto/tls`,
  `httputil`); the existing `proxy/` keeps using goproxy v1.9.4 until the
  cutover.
- The control API is mTLS and reachable only from the host.
- In `run` mode the proxy container restarts on its own (`unless-stopped`);
  in daemon mode (`up`) it doesn't (`NoRestart`).
- The host is trusted; the sandbox is not; a cloned repo's project config is
  written by whoever wrote the repo.

## Approaches considered

**When to terminate TLS.**
- *Only where needed (selected):* a `host:port` is terminated when a request
  rule or secret applies to it, or audit mode is on. Nothing else changes, so
  pinned clients and tools with bundled roots keep working everywhere else.
- *Explicit `inspect:` list:* clear, but every rule needs two edits.
- *Terminate everything with a passthrough list (OpenShell, srt):* full
  visibility, but breaks pinned/rustls tools until listed.

**Rule format.**
- *Mixed string/object entries in `allow-http`/`deny-http` (selected):* one
  place for policy; older binaries fail to parse objects and refuse to start
  (fail closed).
- *Separate `http-rules:` key:* smaller change, but older binaries ignore the
  unknown key and silently drop path denies (fail open).
- *Compact strings (`"host:443 GET /path"`):* stays `[]string`, but `when`
  and long path lists don't fit.

**TLS termination implementation.**
- *Standard library (selected, after a spike):* `http.Server` accepts
  `CONNECT` and hijacks the connection; tunnels are a copy loop; terminated
  connections go through `tls.Server` and a one-connection `http.Server`
  (HTTP/1.1 and HTTP/2 through one handler); `httputil.ReverseProxy` with a
  pinned `http.Transport` forwards. The security rules are properties of the
  structure: only `tls.Server` ever reads a terminated connection, so
  cleartext can't be served, and one handler sees every request.
- *goproxy `MitmConnect`, tightly wrapped:* first choice because it meant the
  smallest change to working code. That reason fell away once `netproxy`
  became a new module. Its MITM path makes decisions that have to be
  corrected: it prefers the inner `Host`, `:authority`, or an absolute-form
  URL's host over the `CONNECT` target, serves cleartext and h2c after
  `CONNECT`, defaults its transport to `ProxyFromEnvironment`, and dials
  tunnels without our checks. Each needs a workaround that depends on
  goproxy's internals staying the same.
- *Spike result (throwaway, not kept in the repo):* both versions passed the
  same 11 cases (whole-host allow plus path deny through ordinary `http://`,
  HTTP/1.1 and HTTP/2 inside `CONNECT`; path-allow-only host; `https://`
  ordinary request rejected; tunneling without rules; host mismatch;
  absolute-form inside the tunnel; cleartext after `CONNECT`; dial pinning on
  reconnect), stable over 10 runs and under the race detector. Disabling each
  guard in turn confirmed the tests catch it; with goproxy, cleartext after
  `CONNECT` reached a plain-HTTP upstream once its guard was removed. goproxy
  needed 72 lines with 5 corrections; the standard-library version 183 lines,
  the difference being plumbing (hijack, copy loop, one-connection listener,
  certificate cache) rather than security logic.
- goproxy's years of fixes are the checklist for the edge cases the new code
  owns; see "Edge cases from goproxy's history" in section 1.

**Condition language for `when`.**
- *CEL or expr (selected):* boolean expressions, no I/O, type-checked at load.
  CEL preferred; `expr` if cel-go grows the binary too much.
- *Rego:* a whole-policy language; "undefined" semantics are dangerous for
  deny rules, has I/O built-ins, and is the heaviest dependency.

## Design

### 1. TLS termination

**When.** At `CONNECT`, after the existing host-level checks, the proxy
terminates TLS for the `host:port` if any of these holds:

- an allow or deny entry in object form with `methods`, `paths`, or `when`
  matches the host and port;
- an allow entry with `secret` matches the host and port;
- audit mode is on and the target is an allowed host on port 443, other than
  `host.vibepit`. Other ports may carry non-HTTP protocols that termination
  would break, so in audit mode they stay tunneled and are logged at the
  connection level.

Otherwise it tunnels as today. Rule changes at runtime apply to new
connections; open tunnels stay as they are. Rule hosts with pinned clients are
unsupported and documented as such. Request rules or secrets on
`host.vibepit` are a config error: no public CA certifies it, and upstream
verification has no skip option.

**MITM CA.** A separate per-session CA, not the control-API CA (whose key is
discarded after signing). The CLI generates it with the other session
credentials and delivers its key to the proxy the same way as the control
API server key, so a proxy restart keeps the same CA and the sandbox's trust
stays valid. Leaf certificates use ECDSA P-256 (many clients reject Ed25519
server certs) and are cached per host. Each leaf names only its `CONNECT`
host, gets a random serial, and is valid for a short period well under
browser and OS limits (Chrome rejects leaves valid longer than 398 days, as
goproxy found in #432).

**Sandbox trust.** The CA certificate is mounted read-only into the sandbox.
The entrypoint concatenates the system bundle and the CA into a file under
`/tmp` and the container env points these at it: `SSL_CERT_FILE`,
`REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `PIP_CERT`,
`CARGO_HTTP_CAINFO`, `AWS_CA_BUNDLE`, `DENO_CERT`; `NODE_EXTRA_CA_CERTS`
points at the CA alone. For Java, a truststore setting replaces the JDK's
default store, so it must contain the public roots too: when a JDK is found,
the sandbox copies the JDK's `cacerts` into `/tmp`, imports the CA with
`keytool`, and only then adds the `-Djavax.net.ssl.trustStore` flags to
`JAVA_TOOL_OPTIONS` and the Maven settings. Without a JDK the flags are not
set, because a truststore path that doesn't exist would break every Java TLS
connection. The CA is installed
in every session (rules can arrive at runtime). The CA never goes into the
home volume.

**Request forms.** The new proxy accepts exactly two:

- `CONNECT`, which is the only way to reach an HTTPS destination.
- Ordinary proxy requests with an absolute `http://` URL, for plain HTTP
  (apt and some package mirrors need it). These never carry credentials.

An ordinary proxy request with an absolute `https://` URL is rejected with a
400 that tells the client to use `CONNECT`. Today's `DoFunc` accepts it and
makes the TLS connection upstream itself; common clients (curl, git, npm,
pip, Go, Node, Java with `https.proxyHost`) use `CONNECT` for HTTPS anyway.
Dropping it leaves one way into HTTPS, so credentials only ever travel inside
terminated `CONNECT` connections.

**Two decisions.** The proxy makes two separate decisions, each in exactly
one place:

1. **Host admission**, shared by both entry points: at `CONNECT`, and for
   each ordinary `http://` request (which has no `CONNECT` phase; today's
   `checkRequest` does this for both). It applies host-level allows and
   denies and holds, and yields refuse, tunnel, or terminate (`CONNECT`) or
   refuse or continue (ordinary request). A host admitted only by path allows
   is terminated, so its requests can be authorized. Alias (CNAME) deny
   checks and CIDR checks are not done here once; they run on every upstream
   dial (below), so they still hold on a reconnect.
2. **Request authorization**, for every request the proxy forwards: request
   rules, audit mode, inspectors, then secret injection. It runs for every
   request form: ordinary `http://` proxy requests, HTTP/1.1 inside a
   terminated `CONNECT`, and HTTP/2 streams. The same upstream request gets
   the same decision however it reaches the proxy.

**Structure.** `http.Server` accepts proxy requests. For `CONNECT` it runs
host admission, then hijacks the connection and answers with the client's
HTTP version. A tunnel is a copy loop with half-close. A terminated
connection is handed to `tls.Server` (leaf from the session CA, ALPN `h2` and
`http/1.1`), then served by a one-connection `http.Server` whose single
handler does request authorization for HTTP/1.1 and HTTP/2 alike, and
forwards with `httputil.ReverseProxy` over a `http.Transport` that uses the
pinned dialer. Ordinary `http://` requests use the same handler path after
host admission.

**Inside a terminated connection.**
- The request's authority must equal the `CONNECT` target (default port
  applied): `r.Host` (the `Host` header or HTTP/2 `:authority`) and, for an
  absolute-form request, the URL's host and scheme `https`. A mismatch gets a
  `421 Misdirected Request`, never followed. 421 is also what tells an
  HTTP/2 client that tries to reuse ("coalesce") the connection for another
  host to open a new one; goproxy serves coalesced requests
  (`TestMitmHTTP2Coalescing`), the new proxy deliberately does not.
- The client must speak TLS. Only `tls.Server` reads a terminated connection,
  so cleartext and h2c are never served; a non-TLS first byte closes the
  connection.
- Blocked requests get a real `403` with an explanatory body (today a blocked
  HTTPS host only shows a failed connection).
- HTTP/1.1 and HTTP/2 are both served. WebSocket upgrade requests are
  evaluated like any request; frames are not inspected.
- Upstream certificates are always verified against system roots; there is no
  option to skip verification. The proxy image must carry CA roots.
- **Every upstream dial connects to a validated address.** Today the CIDR
  check and the dial resolve the hostname separately, so the address dialed
  can differ from the one checked; a terminated connection can also open
  several upstream connections over its life. The dialer resolves the
  hostname once per dial, getting both the CNAME chain and the addresses
  (the proxy already queries upstream DNS directly for aliases), checks the
  chain against deny rules and every address against the CIDR rules, and
  dials one of those addresses, keeping the hostname for TLS verification.
  This applies to tunnels and ordinary requests too.
- **The one exception is the authorized `host.vibepit` rewrite.** When host
  admission rewrites `host.vibepit` on an allowed port to the configured
  gateway, the dialer connects to exactly that gateway address without the
  CIDR check, as today. Any other name that resolves to the same private
  address stays blocked.
- The proxy never follows redirects; the client's follow-up request is checked
  again.

**Edge cases from goproxy's history.** goproxy's fixes over the years are the
checklist for what the new code owns. Each item names the goproxy fix
(release and PR, from its release notes and tests in v1.9.4), how the new
proxy handles it, and needs a test. Where the standard library handles it,
the test pins that our glue doesn't break it.

- **Early client bytes after `CONNECT`** (v1.9.3 #814, MITM and tunnel;
  `TestConnectEarlyClientBytes`): a client may send its TLS ClientHello in
  the same write as `CONNECT`; those bytes sit in the server's read buffer and
  are lost if the tunnel or handshake reads the raw socket. The hijacked
  `bufio.Reader` is drained first by both the copy loop and `tls.Server`.
  Test both paths with `CONNECT` and ClientHello in one write.
- **`CONNECT` reply line** (v1.9.1 #804, issue #802; v1.8.2 #749 wrote
  `HTTP/0.0`): reply with the client's HTTP version and
  "200 Connection established".
- **`Expect: 100-continue`** (v1.9.3 #810; `TestMitmSendsContinueBeforeReadingBody`,
  `TestMitmDoesNotSendContinueAfterFinalResponse`): without a `net/http`
  server on the terminated connection nobody sends `100 Continue` and the
  client waits forever; after a final response it must not be sent, and the
  connection must be closed rather than left waiting for a body that never
  comes. `http.Server` does all three; a request refused before its body is
  read gets its 403 without `100 Continue`. Test an allowed upload, a refused
  one, and a body-dependent `when` rule.
- **WebSocket upgrades** (v1.7.0 #637 rewrite; v1.9.2 #805 early first frame;
  v1.2.2 #606 default port; v1.2 #505 scheme): `ReverseProxy` handles HTTP/1.1
  `Upgrade` by switching to a bidirectional copy. Test a WebSocket through a
  terminated connection and through a tunnel, including a first frame sent in
  the same write as the upgrade request (bytes buffered at hijack must not be
  dropped; wrap if `ReverseProxy` loses them). WebSockets over HTTP/2
  (extended `CONNECT`, RFC 8441) are not supported in the first version.
- **HTTP/2** (v1.9.0 #785 MITM; v1.8.1 #742 missing `Content-Length` from an
  HTTP/2 upstream to an HTTP/1.1 client; v1.8.3 #755 upstream protocol version
  leaking into the response; v1.7.1 #651 ALPN; tests for concurrent streams,
  h2c, coalescing): test HTTP/1.1 client with HTTP/2 upstream and the
  reverse, concurrent streams on one connection, and that the client sees its
  own protocol version. h2c and coalescing are refused (above). `CONNECT` sent
  to the proxy over HTTP/2 is out of scope: the sandbox talks HTTP/1.1 to the
  proxy.
- **Bodiless responses** (v1.9.1 #797; v1.8.4 #770 304, #776 `NoBody`; v1.2
  #473 HEAD `Content-Length`): HEAD, 204, and 304 must never be framed as
  chunked, or the stray framing corrupts the next response on a kept-alive
  connection. Test each, over HTTP/1.1 and HTTP/2 upstreams, followed by a
  second request on the same connection.
- **Response head and headers** (v1.8.5 #787 head in one write, since many
  tiny TLS records broke strict WebSocket clients; v1.8.5 #791 and v1.8.4
  #771 headers overwritten; v1.8.4 #773 trailers): `http.Server` buffers the
  head; `ReverseProxy` forwards trailers. Test trailers end to end (gRPC
  relies on them) and multi-value headers.
- **Body lifecycle and streaming** (v1.9.3 #812 body closed twice; v1.8.1
  #744 request body not closed on a custom response; v1.8.6 #794 streaming;
  v1.2 #523 flushing; `TestMitmResponseStreamsSSE`): stream bodies both ways
  without buffering (except the capped buffering for `when` rules that read
  the body), close every body once, and flush server-sent events as they
  arrive (`ReverseProxy` flushes `text/event-stream` immediately). Test an SSE
  stream that delivers events before the upstream finishes.
- **Connection close and keep-alive** (v1.9.3 #815 `Connection: close`;
  v1.8.0 #734 keep-alive; v1.8.4 #779 close when the upstream errors; v1.2.1
  #587/#588 non-persistent connections; v1.2 #541 close on failed handshake):
  `http.Server` handles `Connection: close` and keep-alive; the
  one-connection listener and the hijacked socket are closed when the server
  is done or the handshake fails. Test each, with a raw client that can tell
  whether the proxy closed.
- **Request cancellation** (v1.6.0 #634; v1.2 #483/#462 contexts): a client
  that goes away cancels the upstream request through the request context.
  Test that a disconnect mid-request ends the upstream request.
- **Proxy and hop-by-hop headers** (v1.2 #460 `KeepHeader`): `ReverseProxy`
  removes hop-by-hop headers, including `Proxy-Connection` and
  `Proxy-Authorization`. The proxy adds no `X-Forwarded-*` headers.
- **Header canonicalization** (v1.2.4 #607, v1.2 #553): goproxy offers to keep
  header-name case. `net/http` canonicalizes HTTP/1.1 header names and HTTP/2
  lowercases them anyway; accepted and documented.
- **Malformed requests** (v1.9.2 #809 panic on a malformed URL; v1.8.6 #793
  unexpected scheme): the `net/http` parser rejects malformed requests before
  the handler; the handler validates scheme and authority. Fuzz the handler's
  target parsing.
- **Certificates and upstream TLS** (v1.2 #432 validity too long, #466
  invalid certificates, #355 random serials; v1.8.4 #774 `InsecureSkipVerify`
  on the origin transport; v1.9.4 #822 leaf key derivation): short-lived
  leaves with random serials and fresh random keys; upstream verification
  always on.
- **Concurrency** (v1.2 #509 data race in MITM handling): run the proxy tests
  under the race detector.
- **Timeouts** (no goproxy fix, but its inner MITM loop sets none): the
  one-connection servers set read-header and idle timeouts so an idle
  terminated connection doesn't hold resources forever.

### 2. Rule model

`allow-http` and `deny-http` accept strings (unchanged) and objects, in
global config, project config, presets, the proxy config JSON, and the
control API:

```yaml
allow-http:
  - github.com:443                    # whole host, tunneled
  - host: api.github.com:443
    methods: read-only                # or a list: [GET, POST]
    paths: ["/repos/{my-repos}/**"]
    when: 'request.param("ref") != ""'
    secret: github                    # allow entries only
deny-http:
  - host: api.github.com:443
    methods: [DELETE]
    paths: ["/**"]
```

A new `HTTPEntry` type, decodable from a string or an object, replaces
`[]string` through config, `ProxyConfig`, the control API, deduplication
(`EntryKey`), and `DenyRules`. A string is a host-level entry.

**Semantics.**
- A host-level allow lets every request to that host through.
- Allows add up; any matching deny wins. As in today's `proxy/policy.go`,
  one policy type in `netproxy` is the only place decisions combine, now with
  a request-level decision too.
- A host with only path allows refuses unmatched requests with a 403
  "not allowlisted".
- A host-level deny blocks at `CONNECT`; a path deny blocks the requests it
  matches.
- Path allows on a host that a host-level allow already covers are shadowed;
  vibepit warns at startup and the monitor shows it.
- A deny entry with `secret` is a config error.

**Path matching.**
- `*` matches one segment; `**` matches zero or more whole segments
  (`/repos/x/**` covers `/repos/x`). No `?` or character classes.
- Normalization applies only when a decision depends on a path pattern for
  that host (path rules, or a secret's inject paths). Other terminated
  requests, such as audit-mode traffic on hosts without rules, are forwarded
  untouched. Scoped npm packages (`/@scope%2fname`) and GitLab-style
  `group%2Fproject` paths keep working.
- Two interpretations are computed: (a) the percent-decoded path with `%2F`
  and `%5C` kept inside their segment, and (b) the path with `%2F`/`%5C` also
  decoded as separators. An upstream router may use either, so:
  - an allow, and a credential's inject paths, match only if **every**
    interpretation is in scope;
  - a deny matches if **any** interpretation matches.
- Reject (do not rewrite) requests with `.` or `..` segments, `//`, or NUL in
  either interpretation when a path pattern is evaluated, so
  `/repos/acme/api/..%2F..%2Fother` can't match an allow on
  `/repos/acme/api/**` and reach `/repos/other` with a credential. Forward
  the original bytes so signed URLs keep working. Encoded package names such
  as `/@scope%2fname` pass whenever both interpretations fit the rule (e.g.
  under `/**`).
- Tests check authorization and injection against the path the upstream
  actually receives.
- Allows match case-sensitively; denies match case-insensitively.
- Path patterns don't match query strings; use `when`.
- Methods omitted means any. `read-only` means GET, HEAD, OPTIONS. No
  `read-write`.

**`when` conditions.**
- CEL (cel-go) or `expr`, chosen by measuring the binary size increase. The
  environment and rules are identical either way.
- Environment: `request.method`, `request.host`, `request.port`,
  `request.path`, `request.param(name)`, `request.header(name)`,
  `request.json` (request body parsed as JSON, only when the expression
  references it), and `request.graphql.queryOnly`.
- A condition that references `request.path` is a path decision and follows
  the path rules above, even on a rule without `paths`: the request is
  rejected if either interpretation contains `.`, `..`, `//`, or NUL, and the
  condition is evaluated once per interpretation. An allow matches only if it
  is true for every interpretation; a deny matches if it is true for any.
- `request.graphql.queryOnly` is true only when the body is a single GraphQL
  request (not a batched JSON array) whose document, parsed with a real
  GraphQL parser (`gqlparser`), contains only query operations. A text check
  is not acceptable: comments or a fragment before `mutation` would get past
  it. Parsed only when an expression references it.
- Bodies are buffered only for rules that reference `request.json` or
  `request.graphql`, up to
  64 KiB. Body too large, unparseable, or an evaluation error: an allow rule
  does not match; a deny rule matches.
- Compiled and type-checked at config load (and on runtime adds). Errors stop
  startup or reject the API call.
- A condition only narrows its rule; it never decides which hosts a rule
  covers.

### 3. Sets

```yaml
sets:
  my-repos: [bernd/vibepit, acme/*]
```

- Defined in global and project config; sets with the same name merge, like
  allow lists do today.
- Referenced as `{name}` inside paths. Expanded at load into one pattern per
  entry, so the proxy, holds, and TUI only see concrete patterns.
- Entries are path fragments that may span segments and may contain `*`, not
  `**`.
- A reference to an undefined set is a startup error.

### 4. Presets

**Using presets** (global or project config). Strings stay valid:

```yaml
presets:
  - github-read                       # defaults, no secret
  - uses: github-read
    secret: github
    with:
      repos: [acme/api, acme/web]
  - uses: github-write
    secret: github
    with:
      repos: private-repos            # a set name works where a list is expected
```

A preset can be used several times with different inputs.

**Defining presets** (built-in ones embedded in vibepit, moving from today's
`proxy/presets.yaml`; or user-defined in global config with the same schema;
not in project config):

```yaml
- name: github-read
  description: Read access to GitHub repositories
  inputs:
    repos:
      type: list
      default: ["*/*"]
      description: Repositories as owner/name; * allowed
    host:
      type: string
      default: github.com
  replaces: [vcs-github]
  allow-http:
    - host: "{host}:443"
      methods: [GET]
      paths: ["/{repos}.git/info/refs", "/{repos}/info/refs"]
      credentials: true
    - host: avatars.githubusercontent.com:443
```

- Input types: `list` (substituted into paths like a set; accepts an inline
  list or a set name) and `string` (the only type allowed in `host`).
- An input without `default` is required. Unknown inputs, wrong types, and
  missing required inputs are startup errors. An empty list is allowed and
  expands to no patterns.
- `secret` is accepted by every preset and applies only to rules marked
  `credentials: true`. If the secret's service doesn't cover a credential
  rule's host and paths, that's a startup error.
- `replaces` removes the named preset wherever it appears, including inside
  `default`. A replacing preset must include the whole-host entries it does
  not narrow.
- Existing `domains:` and `includes:` keep working.
- The GitHub presets assume git over HTTPS. Outbound SSH (for example
  `github.com:22` through a `CONNECT`-based `ProxyCommand`) can't be
  inspected, is always tunneled, and would bypass any repo or method
  narrowing. The preset docs say to use HTTPS remotes and not to allow `:22`.
- An unknown preset name is a startup error (today it is silently skipped).
- Accepted risk (vibepit is 0.x): older binaries silently skip unknown preset
  names, so a project using `github-read` opened with an older binary falls
  back to whole-host `vcs-github` via `default`. The docs tell users to update
  vibepit before using narrowing presets. The string shorthand stays valid for
  all presets.
- Built-in presets to add:
  - `github-read`: git fetch, REST reads, raw and codeload downloads, LFS
    download via `when` on `request.json.operation`, website browsing without
    credentials. No `/graphql`.
  - `github-write` (`repos` required): git push, REST writes, LFS upload. No
    `/graphql`.
  - `github-graphql-read`: `POST /graphql` with credentials and
    `when: request.graphql.queryOnly`, so `gh` works for reads.
  - `github-graphql-write`: `POST /graphql` with credentials, mutations
    included.
- `repos` stays an honest limit: every rule a `github-read`/`github-write`
  instance creates is scoped by it. GraphQL serves every repository through
  one endpoint, so it can't be scoped by `repos`; the GraphQL presets take no
  `repos` input and reach everything the token can. Their descriptions, the
  docs, and the trust approval say so ("all repositories this token can
  access"). GitHub requires authentication for GraphQL, so both presets
  require a `secret`.
- The `default` preset is unchanged.

### 5. Secrets and services

**Service definitions** describe how a service authenticates, with no values.
Built-in ones are embedded in vibepit (not `netproxy`), in a new
`services.yaml`; `netproxy` receives definitions as data:

```yaml
- name: github
  env: [GH_TOKEN, GITHUB_TOKEN]
  inject:
    - kind: http-request
      hosts: [api.github.com]
      paths: ["/**"]
      header: Authorization
      auth: bearer
    - kind: http-request
      hosts: [github.com]
      paths: ["/*/*.git/**", "/*/*/info/refs", "/*/*/git-upload-pack", "/*/*/git-receive-pack"]
      header: Authorization
      auth: {basic: x-access-token}
```

Every inject entry names its `kind`, and it is required: there is no
default. The kind decides which other fields the entry takes and which
flows it applies to. This project has one injectable kind, `http-request`,
with the fields above. A later kind (for example `ssh-session`) adds its own
entry type alongside without changing existing definitions; the trust
approval lists grants per kind. Everything around injection is shared by all
kinds: sources, delivery, placeholders and their detection, trust approval,
redaction, and binding to the targets the definition allows. Only applying
the credential to a flow is per kind.

`auth` styles for `http-request`: `bearer`, `token`, `{basic: <user>}`,
`raw`.

Each `http-request` inject entry binds to an **HTTPS origin**: scheme
`https`, the listed hosts, and an explicit port (`port`, default 443). A rule
with `secret` whose port differs from every binding is a config error. An
inject entry without `kind`, or with a kind that isn't injectable, is a
config error.

**Secrets** are configured in global config only:

```yaml
secrets:
  github:                             # uses the built-in "github" service
    source: {command: "gh auth token"}
  my-api:                             # inline service
    source: {env: MY_API_KEY}
    env: [MY_API_KEY]
    inject:
      - kind: http-request
        hosts: [api.example.com]
        header: X-Api-Key
        auth: raw
```

Sources: `env` and `command`.

**Delivery.**
- `run` and `up` resolve sources on the host at startup and push values to
  the proxy through a new mTLS control API endpoint. Values are held in proxy
  memory only, never in `ProxyConfig`, container env, or any response.
- `vibepit secret refresh` resolves and pushes again (for expired tokens).
- After a proxy restart (in `run` mode, which restarts automatically) no
  values are loaded. Requests that need a secret get a 403: "secret `github`
  not loaded, run `vibepit secret refresh`".

**Placeholders.** One random placeholder per secret per session (for example
`vibepit-secret-github-<random>`), stable for the session, set in the sandbox
env for each name in the service's `env`. It exists so tools like `gh` run;
it carries no value.

**Injection.** For a request inside a terminated `CONNECT` connection that
passes request authorization (the only request form that can carry a
credential):
- If an allow rule with `secret: X` matches, the request is allowed overall
  (no deny matches), and the host and path fall within one of X's `inject`
  entries, the proxy overwrites that entry's header with the formatted value.
- Immediately before attaching, the proxy checks the final outbound URL:
  scheme `https` and authority equal to the binding's host and port. Plain
  `http://` requests, cleartext inside `CONNECT`, and any other port never
  carry a credential upstream.
- If two different secrets would set the same header on one request, block
  it and log the conflict.
- Otherwise, if the service's header carries the placeholder (decoding Basic
  auth), strip the header and forward unauthenticated.
- A placeholder found anywhere else the proxy looks (other headers, path,
  query) blocks the request and raises an alert in the monitor. Bodies are
  not scanned. Placeholders sent to tunneled hosts are invisible; they are
  worthless, so this is a detection signal, not a protection. This check is
  implemented as the first request inspector (section 6); stripping the
  service's own header stays part of injection.

**Project trust.** A project config may use `secret` on rules and presets, and
add entries to sets used by secret-bearing rules, only after the user
approves it:
- **What is approved is the effective credential policy**, and the prompt
  shows exactly that. vibepit expands the merged config (global plus
  project, sets and presets resolved, built-in presets included) into a
  canonical list of credential grants: for each secret, the HTTPS origin,
  methods, path patterns, `when` conditions, and every deny on that origin.
  - Approval is needed when the project contributes to that list: the list
    computed with the project config differs from the list without it.
  - The approval is bound to the project's identity (its resolved root path)
    and a hash of the canonical list, stored on the host outside the repo.
  - So anything that changes the list asks again: a project edit, removing a
    deny that narrowed a grant, a new set entry, a vibepit upgrade whose
    built-in preset widens a grant, or a global edit that changes a grant the
    project contributes to. Changes that leave the list identical (such as
    allows without `secret`) don't ask.
- The prompt lists the grants per secret. Grants that path patterns can't
  limit, such as the GraphQL presets, are flagged as "all repositories this
  token can access".
- The check runs where the project config is loaded: when `run` starts a new
  session, it asks if the hash is new or changed; `up` refuses with a hint to
  run a new `vibepit trust` command. `connect`, and `run` attaching to a
  running session, don't load config and don't ask.
- Declining aborts startup with a message naming the secret-related parts.
  There is no "start without them" mode.
- The agent can't change the project config mid-session: `.vibepit` is
  hidden in the sandbox behind a read-only mount, and config is read only at
  startup.
- Project config without secret-related parts works exactly as today.

### 6. Request inspectors

An extension point for detecting exfiltration attempts, so secret scanners
can be added later. This project builds the interface and its place in the
request path; the placeholder check is the only inspector.

- **Position in the pipeline:** rule evaluation, then inspectors, then
  secret injection, then forwarding. Inspectors see the request exactly as
  the agent sent it; running after injection would flag every injected
  token.
- **Interface (in `netproxy`):** an inspector receives the request target,
  method, path, query, and headers, and returns findings. Each finding names
  the inspector and rule, the location (header name, path, query, or later a
  body offset), and an action: alert (log and continue) or block (403 with a
  block code). A finding never contains the matched value; at most a short
  prefix or a hash.
- **Room for later, without changing existing inspectors:** streamed,
  detect-only body access (with `Content-Encoding` decoding), buffered bodies
  for blocking with a size cap, external scanner adapters (ICAP or
  Envoy `ext_proc` style), and in-process scanners such as gitleaks' rules.
- **Wire types:** a finding is carried on the flow record and the `/events`
  stream, so the monitor and other clients can show it.
- **vibepit chooses the inspectors;** `netproxy` ships none besides what the
  caller registers. With no inspectors registered, no inspection work runs.
- **Inspectors only tighten.** A finding can alert or block; nothing an
  inspector returns can allow a request that policy refused.
- **Coverage:** inspectors see every request that goes through request
  authorization: terminated connections and ordinary `http://` proxy
  requests, not tunnels. Broad scanning
  will need broad termination (allowed hosts on 443, except `host.vibepit`).
  That scope is one mechanism, built now for audit mode, which a future scan
  switch also turns on. Tunneled traffic stays invisible, and the threat
  model says so.

### 7. Audit mode

- Enabled with `--audit` on `run`/`up`, or `audit: true` in the global
  config. It is a host-controlled setting: `audit` in a project config is a
  config error, so a cloned repo can't relax path allows.
- Terminates TLS on every allowed host and logs every request with method
  and path.
- Requests refused only for not matching a path allow are let through and
  logged as "would block".
- Not relaxed: the host allowlist (unlisted hosts are still blocked or held),
  host-level denies, explicit path denies, and inspector blocks (such as a
  placeholder sent outside its binding).
- Audit mode lives in the policy, not in an inspector: it relaxes a decision,
  and inspectors may only add alerts or blocks, never turn a block into an
  allow. What it shares with future scanning is the broad termination scope
  (section 6).
- Secret injection works as in normal mode.
- Monitor "draft rules" action: from selected request entries, generate
  object-form `allow-http` YAML grouped by host and method with path
  patterns, for the user to edit into a preset.

### 8. Logging and events

- Request-level records gain method, path, query parameter names, status,
  mode (tunnel/mitm), secret (name only), and an audit marker. On the wire
  they are the details of `http-request` flows (see "A generic protocol"
  below).
- Query strings: parameter names only, in every mode including audit. Signed
  download URLs carry credentials vibepit doesn't know about. Draft rules
  need only host, method, and path.
- Never logged: headers, bodies, query values, secret values, placeholders. A
  placeholder block logs "placeholder for secret `github` sent outside its
  binding".
- Flow records are sanitized when they are built, before they enter a ring
  buffer or `/events`: known placeholders and secret values are replaced with
  `<secret:NAME>` in every retained field derived from the request (host,
  path, query parameter names, reason text). Redacting only the inspector's
  finding is not enough, since the request entry itself carries those
  fields.
- New block codes for request-rule refusals, host mismatch, placeholder
  misuse, and missing secret values. `AllowFixes` is false for all except a
  request-level "not allowlisted".
- `http-request` flows go into their own ring buffer (10k), so floods don't
  push out host-level blocks. `/events` carries both.
- Target rows count flows per kind (connections for `http-connect`, requests
  for `http-request`, queries for `dns-query`, connections for `host-port`).

**A generic protocol: kinds, targets, flows.** The control API and event
stream are not written per protocol, so future network use cases don't need
a new stream or new endpoints. Today's model is halfway there (`Source` is
`proxy` or `dns`, `HostKey` is a target), but event types, the host table,
holds, and endpoints such as `/allow-http` and `/allow-dns` are each
protocol-specific.

- **Kind.** Every network use case is a kind. This project has four:
  - `http-connect`: a `CONNECT` or ordinary `http://` request, decided at
    host level; target `{host, port}`.
  - `http-request`: a request inside a terminated connection; target
    `{host, port}`, details method, path, query parameter names, status.
  - `dns-query`: target `{domain}`, details query type.
  - `host-port`: sandbox-to-host traffic through `host.vibepit`; target
    `{port}`.

  A kind defines its target (a normalized key), its details (a typed
  struct), which rules apply to it, and whether it can be held, allowed, or
  inspected. Later kinds (outbound SSH, raw TCP or UDP, MCP calls, inbound
  forwards) add a value and two structs; the stream, envelope, event types,
  and endpoints stay the same.
- **Typed, not a bag of maps.** Each kind has a Go struct for its target and
  one for its details, with `kind` as the discriminator. No
  `map[string]any`. TypeScript types generate as discriminated unions.
- **Flows.** One decision about one flow of any kind is a flow record:
  `{kind, target, decision, code, reason, rule, detail, ...}`. It replaces
  `LogEntry` on the wire; the fields from section 8 above (method, path,
  status, mode, secret, audit marker) live in the `http-request` details.
- **Parent flows.** A flow record has an optional `parent`: the ID of the
  flow it belongs to. An `http-request` names the `http-connect` of its
  terminated connection, so the monitor can group requests under their
  connection. Later kinds use the same field (an MCP call inside an
  `http-request`, a channel inside an SSH session).
- **Targets.** The host table becomes a target table: rows keyed by
  (kind, target) with counts and flags (allowed, denied, rejected, held).
  DNS and HTTP rows for the same domain stay separate, as today.
- **Generic holds.** Any kind whose decision can wait can be held. In this
  project: `http-connect` and `host-port`. A hold's key is (kind, target).
- **One decision endpoint.** `POST /decisions {kind, target, action}`, with
  action `allow`, `deny`, or `reject`, replaces `/allow-http`, `/allow-dns`,
  `/deny`, `/deny-http`, and `/deny-dns`. The proxy turns the target into a
  rule of that kind, applies it, and returns the rule in canonical form for
  vibepit to save to config. Clients need no rule syntax. Object-form rules
  (methods, paths, `when`) from the CLI go through a rules endpoint that takes
  a kind and a rule.
- **Unknown kinds degrade cleanly.** A client that doesn't know a kind shows
  the kind, the target as a string, and the decision. Each target carries an
  `allowable` flag from the proxy; an older client can still allow it through
  `/decisions` without understanding the kind.

**How future kinds fit.** Not built in this project; recorded to check that
the protocol holds up.
- **Raw TCP egress** (e.g. `psql` to a cloud database): a new entry point
  (SOCKS5 or per-port listeners) and a kind such as `tcp-connect` with target
  `{host, port}`. It reuses host admission, the per-dial check (CIDR,
  aliases), holds, policy, and the stream. The kind is declared not
  inspectable: no termination, no request rules.
- **SSH:** as plain TCP, the same. As an SSH-aware proxy (terminating SSH the
  way the proxy terminates TLS), an `ssh-session` kind with user and command
  as details (e.g. `git-upload-pack 'acme/api'`) would allow repo rules for
  git over SSH; channels are child flows via `parent`.
- **Inbound forwards** (host to sandbox) and **MCP calls** (child flows of
  `http-request`) fit as kinds as well.
- **Secret injection is per kind by nature.** Today only `http-request` is
  injectable (HTTP headers). Credentials for other kinds (an SSH key through a
  signing agent on the host, a database password) need their own inject
  entry type, selected by the entry's required `kind`. Existing definitions
  stay valid, but each kind's injection is real work.

**The `host-port` kind.** Today `host.vibepit` is an HTTP target with special
handling: a blocked `host.vibepit:3000` is held, and allowing it writes
`allow-http: host.vibepit:3000`. As its own kind:
- Traffic to `host.vibepit:<port>` (through `CONNECT` or an ordinary
  `http://` request) produces `host-port` flows with target `{port}`. A
  `CONNECT` tunnel carries any TCP protocol, so clients that use an HTTP
  proxy reach any host service this way. Clients that ignore `HTTP_PROXY`
  and open raw TCP (`psql`, `redis-cli`, plain `ssh`) can't: the sandbox
  network only reaches the proxy, and the proxy's DNS answers `host.vibepit`
  with the proxy's own IP, where nothing listens on that port. There is no
  SOCKS or transparent TCP proxy, and none is in scope.
- The kind doesn't depend on transport. A later raw-TCP path (SOCKS5, or
  per-port listeners on the proxy IP for allowed host ports) produces the
  same `host-port` flows; the details record the path (`connect`, `http`,
  later e.g. `socks`).
- Rules are `allow-host-ports` (which already exists in project config) and
  a matching `deny-host-ports`. Allowing from the prompt or monitor writes
  `allow-host-ports: [3000]`.
- The prompt shows "sandbox wants host port 3000", not a hostname.
- Existing `allow-http`/`deny-http` entries for `host.vibepit:<port>` are
  read as host-port rules, with a warning to move them.
- `host-port` targets are never terminated (no public CA certifies
  `host.vibepit`), and their dial uses the gateway exception from section 1.
- In `netproxy`, `host.vibepit` is the general host-rewrite configuration
  (section 11): a name, a gateway address, and the kind its traffic belongs
  to.

**Event stream: one versioned state stream.** Today clients build their view
from two sources: they poll `/hosts` (with `X-Vibepit-Last-Log-ID`) and read
`/events`, then merge (`cmd/monitor_hosts.go`: `polled`, `pending`,
`covered`). Policy changes from other clients aren't events, so the monitor
keeps its own guesses until the next poll (`denied`, `rejected`,
`allowlisted`, `allowGen`). A proxy restart is detected by a heuristic (a
cursor lower than the last ID), and a resume point that fell out of the ring
buffer silently loses entries. `netproxy` replaces this with a "list then
watch" stream:

1. **Snapshot, then changes, on one sequence.** The proxy keeps the state
   itself: the target table, open holds, the effective policy of every kind
   including runtime allows and denies, secret status, and session settings
   such as audit mode. A stream starts with that state at sequence N, then
   sends only changes on the same sequence. Clients apply changes to their
   state; there is no separate poll and no merge. `GET /targets` returns the
   same snapshot once, for scripts.
2. **Generic event types.** `snapshot`, `flow`, `hold-open`, `hold-end`,
   `policy` (a rule of any kind added or removed), `target` (a row update),
   `finding`, `session`, and `gap`. The kind is a field inside the data, never
   a new event type. Example:

   ```json
   {"instance":"7f3a…","seq":4812,"type":"flow","data":{
     "kind":"http-request",
     "target":{"host":"api.github.com","port":"443"},
     "decision":"block","code":"request_not_allowlisted",
     "rule":"allow-http[3]",
     "detail":{"method":"POST","path":"/repos/acme/x/issues"}
   }}
   ```
3. **Policy changes are events.** An allow, deny, or reject from any client
   (prompt, monitor, CLI, the desktop app) reaches every subscriber at once.
4. **Proxy instance ID.** A random ID per proxy start is part of every
   snapshot and event. A resume point is instance plus sequence; a different
   instance means a restart, detected for certain.
5. **Gap signal.** If the requested resume point is no longer available, or
   the subscriber was dropped for falling behind, the proxy sends a `gap`
   event and a fresh snapshot instead of silently skipping entries.
6. **Topics and kinds.** A subscriber filters by event type and, optionally,
   by kind: `?topics=holds,policy` for the prompt;
   `?topics=flows&kinds=http-request` for the monitor's request log. Flow
   records of each kind keep their own ring buffer where volume calls for
   it: `http-request` gets its own (10k); `http-connect`, `dns-query`, and
   `host-port` share one, as today. DNS volume stays low because clients that
   use the proxy leave name resolution to it, and the proxy resolves through
   upstream DNS directly, not through the sandbox's DNS server.
7. **Standard SSE framing, uniform envelope.** Each event carries
   `id: <instance>:<seq>`; its data is always `{instance, seq, type, data}`.
   Resume position and filters are query parameters:
   `GET /events?after=<instance>:<seq>&topics=holds,policy`. The standard
   `Last-Event-ID` header exists for browsers' `EventSource`, which can't set
   headers and reconnects to the same URL; no client of the mTLS API is a
   browser, so the header can be added later if one ever is.
8. **Target row updates are rate-limited per target.** The proxy aggregates
   the target table and sends a row's update at most every few hundred
   milliseconds, so clients don't aggregate flows themselves.

**Who turns holds on.** Today a request is held only while some subscriber
is on `/events` (`h.hub.Watching()`), and open holds end as "unwatched" when
the last one leaves. With topics, only subscribers of the `holds` topic count
as watching: a client that reads only the request log must not make requests
wait on a prompt nobody shows.

Kept from today: hold deadlines sent as time remaining (`hold_left_ms`), so
the proxy's and client's clocks don't need to agree; publishing that drops a
slow subscriber instead of blocking; keepalive comments.

### 9. TUI

- Logs tab: method, path (truncated, `tui.SanitizeText`), status, a
  tunnel/mitm marker, a secret-injected marker, a "would block" marker in
  audit mode, and highlighted placeholder alerts.
- Hosts tab: tunnel/mitm marker; shadowed-rule warnings.
- On a refused request entry the monitor offers: allow exact path, allow a
  prefix, deny the path. These write object entries with literal hosts and
  paths to the project config. The monitor never writes `secret`, `{set}`
  references, sets, or presets, so it cannot widen where a secret goes. A
  saved allow never changes the approved credential policy. A saved deny on
  an origin that has a credential grant does change it (denies on that
  origin are part of the list), so the next startup asks for approval again;
  the prompt shows the grant narrowed.
- The approve screen and host-level holds are unchanged in behavior.
- At cutover, the feed (`cmd/feed.go`), the hosts tab, and the prompt are
  rebuilt on the new event stream (section 8): they apply the snapshot and
  changes instead of polling `/hosts` and merging, and drop their local
  guesses about allows, denies, and rejections. This removes most of
  `cmd/monitor_hosts.go`'s merge logic. The hosts tab shows the target table,
  all kinds included; decisions go through `/decisions`.
- The prompt and monitor render the four known kinds specifically (e.g.
  "sandbox wants host port 3000") and any unknown kind generically: kind,
  target as a string, decision, and allow if the target is allowable.
- Draft rules view (section 7).

### 10. CLI and control API

- `allow-http` / `deny-http` commands gain `--method` and `--path` flags that
  produce object entries.
- New commands: `vibepit secret refresh`, `vibepit trust`.
- Control API: `POST /decisions` for allow, deny, and reject of a target of
  any kind; a rules endpoint for object-form rules (kind plus rule); a new
  endpoint receives secret values. `/events` is the versioned state stream
  and `GET /targets` its one-off snapshot (section 8).
- Going away with the old proxy, with their successors: `/allow-http`,
  `/allow-dns`, `/deny`, `/deny-http`, `/deny-dns` (→ `/decisions` and the
  rules endpoint); `/hosts` and `X-Vibepit-Last-Log-ID` (→ `/targets` and the
  stream); `/check`, which the approve screen uses to see whether another
  client already decided (→ policy events and the snapshot); `/config`,
  which returns the whole `ProxyConfig` (→ the effective policy in the
  snapshot); `/logs` (→ the stream).
- `allow-host-ports` gains a matching `deny-host-ports`; the prompt and
  monitor save host-port decisions there.

### 11. Packaging: the `netproxy` module

The new proxy is a nested Go module, `netproxy/` (module
`github.com/bernd/vibepit/netproxy`), that imports no vibepit packages, so it
can be reused in other projects. The existing `proxy/` package stays
untouched until the new one is done.

**In `netproxy`:** policy (allowlists, deny rules, request rules, path
matching, conditions), TLS termination (CA and leaf certificates, `Host`
pinning, tunnel-or-terminate decision), secret injection from service
definitions and values the caller supplies, the request inspector interface
and pipeline stage, holds, event hub, log buffers, DNS server, CIDR
blocking, and the control API as an `http.Handler` with its wire types. It
takes a typed Go config containing only concrete rules. It is built on the
standard library and does not depend on goproxy; its tunnel copy loop is its
own (it can't import vibepit's `netpipe`).

**`netproxy` owns no durable state.** It holds state in memory: open
connections, tunnels and holds; the target table and flow ring buffers;
runtime policy changes (allows, denies, session rejections); secret values;
the leaf certificate cache; the event sequence and instance ID. None of it
survives a restart, which resets everything to the config it is started
with: connections and holds break (clients retry), counters and history are
observability, decisions that were never saved are meant to be temporary,
secrets come back through `secret refresh` (later the daemon), and clients
see the new instance ID and take a fresh snapshot. What must last belongs to
the embedder. That makes `netproxy` disposable: it can be thrown away and
started again with a new config at any time, which is what makes it
embeddable.

- **No files, no persistence, no restart logic** in `netproxy`. It gets a
  typed Go config at construction.
- **A Go API at the core; the control API is its HTTP binding.** Decisions,
  rules, secret values, and event subscriptions with topics are Go methods;
  the HTTP handler is a thin layer over them. Another project can embed
  `netproxy` without the HTTP control API, or with its own.
- **`ApplyConfig(cfg)`** replaces the whole policy atomically at runtime and
  publishes the changes as `policy` events, so an embedder can reload from
  any source without a restart.
- **In-process subscriptions:** an embedder that wants to persist runtime
  decisions subscribes to `policy` events and stores them however it likes.

**Telemetry.** `netproxy` provides operational telemetry (how the proxy
itself is doing) through standard APIs; domain events (flows, holds, policy
changes) stay on the event stream and are not duplicated.

- **Metrics and traces: OpenTelemetry API, no SDK.** `netproxy` depends only
  on the OTel API packages (`go.opentelemetry.io/otel`, `/metric`, `/trace`),
  which pull in neither gRPC nor exporters. The embedder passes a
  `MeterProvider` and `TracerProvider` as options; without them telemetry is
  a no-op. Exporters are the embedder's choice.
- **Logs: `log/slog`.** `netproxy` takes a `*slog.Logger` (standard library,
  no dependencies). Embedders route it anywhere, including OTel through the
  `otelslog` bridge. The OTel Logs API for Go is not used directly.
- **Rejected:** Prometheus `client_golang` (metrics only, ties the library
  to Prometheus; the OTel exporter can serve Prometheus), own hook interfaces
  (would rebuild the OTel API, especially context propagation for traces),
  `expvar` (simple counters only).
- **Low cardinality.** Metric attributes are bounded values such as `kind`,
  `decision`, and `code`, never host or path: the sandbox can generate any
  number of names, and per-target counts are in the target table.
- **Redaction applies.** Span attributes and log fields follow the
  flow-record rules (no query values, headers, bodies, placeholders, or
  secret values) and go through the same sanitizer before they reach a span
  or log record.
- **No trace propagation into agent traffic.** The proxy never adds
  `traceparent` or other trace headers to upstream requests. An incoming
  `traceparent` from the sandbox is untrusted and not used as the parent
  span by default; an option can enable it.
- **Spans:** one per connection (`CONNECT`, tunnel or terminated, or
  ordinary `http://` request), with children per request for admission,
  authorization, inspection, injection, the upstream dial (DNS and the CIDR
  check), and the upstream round trip.
- **Metrics:** active connections and holds; flows by kind and decision;
  latency of dial, TLS handshake, upstream round trip, and hold duration;
  event subscriber drops and gaps; leaf certificate cache size and misses;
  secrets loaded or missing.
- **vibepit's side:** exporting is off by default. A later project can add
  global config for an exporter (OTLP/HTTP rather than OTLP/gRPC, which would
  noticeably grow the proxy binary). The remote branch `origin/add/otlp`
  goes the other way (the proxy receiving agents' OTLP telemetry); that is
  agent-specific, belongs to vibepit, and is not part of this design.

**In vibepit:** config files and merging, expanding sets and presets into
concrete rules (and the preset content), built-in service definitions,
secret sources, project trust, the `ProxyConfig` JSON container format, mTLS
setup from the `VIBEPIT_PROXY_*` env vars, CA common names, user-facing
message text, `host.vibepit`, the SSH forwarder, sandbox trust setup, and the
TUI. Also where the proxy's config lives and what survives a restart:

- Today `bootstrap` writes the proxy config to a temp file
  (`os.CreateTemp("", "vibepit-proxy-*.json")`) mounted read-only as
  `/config.json`; only the TLS credentials travel in the container env. A
  Docker restart re-reads the unchanged file, so rules saved during the
  session are lost until the next session. After a host reboot the temp file
  may be gone; Docker then creates an empty directory at that path and the
  proxy fails to start.
- New: the proxy config lives in the session directory next to the session
  credentials, so it survives a reboot. When the CLI or TUI saves a rule (on
  the host; the proxy only reads the file), it also updates that file, so a
  restart loads saved rules. Unsaved decisions are lost on restart, as
  today.
- `cmd/proxy.go` reads the file, translates it into `netproxy`'s config, sets
  up mTLS, adds the `host.vibepit` rewrite, and starts `netproxy` and the SSH
  forwarder. Watching the file and calling `ApplyConfig` is possible but not
  part of this project.

**Carried over unchanged from today's proxy:**
- The DNS server: filtering by `allow-dns`/`deny-dns`, the CNAME check, the
  CIDR check on DNS answers, `host.vibepit` answered with the proxy IP,
  configurable upstream DNS. DNS queries are never held; resolvers time out
  within seconds.
- Hold limits (at most 32 held targets and 64 waiting requests) and the
  configurable `hold-timeout`. `host-port` holds count toward the same
  limits.
- The target table cap (today `MaxHosts`, 2000), since the sandbox can
  generate any number of names. `http-request` targets are `{host, port}`,
  not paths, so the table stays bounded.
- `block-cidr`/`allow-cidr`, now checked on every dial.
- The control API's mTLS credentials: Ed25519, generated per session, valid
  for 30 days; `netproxy` receives the `tls.Config` from vibepit.

**Things `netproxy` makes configurable instead of hard-coding:** block
message text, host rewrites (`host.vibepit` becomes a general name → gateway
rule whose traffic is the `host-port` kind, with its allowed ports), the
control API's `tls.Config`, and neutral header names (no `X-Vibepit-*`).
The set of kinds is part of `netproxy`'s API; adding a kind is a change to
`netproxy`, not to the stream or endpoints.

**Control API protocol.** HTTP+JSON with the server-sent-event stream, as
today, over mTLS. `netproxy` ships the wire types and a Go client package
(`netproxy/client`) that vibepit's CLI, TUI, and the desktop experiment's Go
backend use. Wire types are plain structs with JSON tags so TypeScript types
can be generated from them (Wails bindings or `tygo`) instead of mirrored by
hand, as `experiment/desktop`'s `bridge.ts` does today. gRPC (dependency
weight, codegen, no `curl`) and ttrpc (local IPC without TLS) were
considered and rejected. Connect RPC is a candidate for the future host
daemon's API, not for `netproxy`.

**SSH forwarder.** Stays in vibepit. It carries host-to-sandbox traffic, has
no policy, and will likely change with the host daemon. `vibepit proxy`
(`cmd/proxy.go`) becomes the glue for the proxy container: it reads
`ProxyConfig`, converts it to `netproxy`'s config, sets up mTLS, adds the
`host.vibepit` rewrite, and starts `netproxy` and the SSH forwarder side by
side. `netpipe` stays in the root module.

**Module mechanics.** The root `go.mod` requires `netproxy` with
`replace ./netproxy` (plus a `go.work` for local work). No `netproxy/v*`
tags until another project uses it. `make test`, `make test-integration`,
CI, and dependency updates cover both modules, since `go test ./...` at the
root skips nested modules. `go install github.com/bernd/vibepit@latest`
stops working because of the `replace`; it is not a documented install path.

**Transition.** A hidden opt-in (flag or env var) on `vibepit proxy` runs
`netproxy` while it is being built; the default stays on `proxy/`. At cutover,
`cmd/` and `config/` switch to `netproxy`'s wire types and `proxy/` is
deleted.

### 12. Docs

- `threat-model.md` (currently states the proxy does not inspect encrypted
  traffic) and `security-model.md`: TLS termination, the MITM CA, secret
  handling, project trust, token scope as the primary control for writes,
  and that OAuth tokens from agent logins still live in the sandbox's home
  volume until the host daemon exists.
- Configuration reference for object entries, sets, presets with inputs,
  secrets, audit mode.

### 13. Behavior the tests must pin

**First acceptance test, written before the `netproxy` extraction begins:**
a whole-host allow plus one explicit path deny, exercised through every
supported request form (ordinary `http://` proxy requests, HTTP/1.1 inside
`CONNECT`, HTTP/2 streams). For each, assert the client gets
a 403 and the upstream receives zero requests. Repeat with a host admitted
only by a path allow: a non-matching request gets a 403 with zero upstream
requests, and a matching request reaches the upstream exactly once, through
each request form. Repeat once more with the path restriction expressed only
in a `when` condition on `request.path`.

- Inner `Host`/`:authority` and absolute-form URL host mismatches are
  rejected; cleartext after a terminated `CONNECT` is rejected.
- Credentials are never sent upstream over `http`, cleartext inside
  `CONNECT`, or a port other than the binding's.
- Each upstream dial, including a reconnect late in a long-lived terminated
  connection, goes to an address that passed the CIDR check, even if DNS
  changes in between.
- Allows and credential paths require every path interpretation in scope;
  `..%2F` traversal is rejected; scoped npm names still pass under `/**`.
- Flow records in the ring buffers and on `/events` contain no query values,
  placeholders, or secret values, including for blocked requests.
- Anything that changes the canonical credential policy asks for approval
  again (project edit, removed deny, built-in preset change); changes that
  leave it identical don't; `audit` in project config is an error.
- Ordinary `http://` proxy requests get the same host admission as
  `CONNECT`: holds, host-level denies, and alias denies.
- An ordinary proxy request with an absolute `https://` URL gets a 400 and
  the upstream receives nothing.
- Alias and CIDR checks run on every upstream dial; the `host.vibepit`
  gateway route works while another name resolving to the same private
  address is blocked.
- A placeholder used as a query parameter name doesn't appear in the ring
  buffer entry or the `/events` output.
- No redirect is followed by the proxy.
- Every item in "Edge cases from goproxy's history" (section 1) has a test:
  early client bytes, `CONNECT` reply version, `100-continue`, WebSockets
  (including an early first frame), HTTP/1.1 and HTTP/2 combinations,
  bodiless responses on kept-alive connections, trailers, SSE streaming,
  connection close, cancellation, hop-by-hop headers, malformed targets,
  certificates, and timeouts. The suite also runs under the race detector.
- A coalesced HTTP/2 request for another host gets a 421.
- Event stream: a client that applies the snapshot and then every change
  ends with the same state as a fresh snapshot; a policy change from one
  client reaches every other subscriber; a restarted proxy (new instance ID)
  and a resume point past the ring buffer both produce an explicit gap and a
  fresh snapshot; a dropped slow subscriber resumes with a gap, not missing
  entries; topics and kinds filter what a subscriber receives.
- Generic protocol: every kind's flows, holds, target rows, and decisions go
  through the same event types and `/decisions`; a client built without
  knowledge of `host-port` can still show and allow a held host port.
- `http-request` flows carry their `http-connect` as `parent`.
- Telemetry: without providers, telemetry is a no-op; with test providers,
  spans and metrics appear with only bounded attributes; no span attribute or
  log field contains query values, placeholders, or secret values; upstream
  requests carry no trace headers; an incoming `traceparent` is ignored by
  default.
- Only subscribers of the `holds` topic enable holds; a subscriber to other
  topics alone gets no holds and no "unwatched" endings when it leaves.
- `ApplyConfig` replaces the policy atomically and publishes `policy` events;
  `netproxy` works without its HTTP control API through the Go API alone.
- A rule saved during a session is in the session's proxy config file, and a
  restarted proxy container enforces it; the file survives a reboot.
- `host-port`: traffic to `host.vibepit:<port>` produces `host-port` flows,
  is held when not allowed, and allowing it writes `allow-host-ports`; old
  `allow-http: host.vibepit:<port>` entries still work, with a warning.
- Hosts without request rules or secrets are tunneled, not terminated.
- Older-format configs produce identical decisions.
- Deny wins over every allow, including preset and secret rules.
- Path normalization rejections; case rules for allows vs denies.
- Body cap and `when` error behavior for allows vs denies.
- Inspectors run before secret injection and see the agent's original
  headers; findings never contain the matched value.
- `request.graphql.queryOnly` is false for mutations, subscriptions, mixed
  documents, mutations after comments or fragments, and batched arrays.
- Secrets never appear in flow records, events, snapshots, API responses, or
  errors.
- Injection only within the service's host and path binding; placeholder
  stripping and misuse blocking.
- HTTP/2 and WebSocket requests through a terminated connection.
- Sandbox trust env and bundle generation (BATS).
- `netproxy` imports no vibepit packages (its own `go.mod` enforces it).

## Future: OAuth with the host daemon

Not built in this project. Recorded so the service format leaves room for it.

**Model (from Docker Sandboxes' "sentinel masking").** The agent logs in as
usual, inside the sandbox. The proxy intercepts the provider's token
endpoint, keeps the real access and refresh tokens, and returns sentinels,
which the agent stores in its credential file. API calls carrying the
sentinel get the real token swapped in. A refresh carrying the sentinel
refresh token is performed with the real one, and the agent again receives
sentinels. This is the only place the proxy would rewrite bodies (the
`refresh_token` request field and the token response), defined per service.

A service definition would gain an optional block along these lines:

```yaml
- name: claude
  oauth:
    token-endpoint: {host: platform.claude.com, path: /v1/oauth/token}
    resource-hosts: [api.anthropic.com]
    response-fields: {access-token: access_token, refresh-token: refresh_token, expires-in: expires_in}
```

**Why it needs the daemon.**
- Real tokens must outlive a session and a proxy restart; otherwise every new
  session means logging in again, since the sentinels persist in the home
  volume.
- Many providers rotate refresh tokens on every refresh, so the newest one
  must be written back to a store on the host (OpenShell's gateway stages the
  replacement before committing it).
- The home volume is shared across sessions, so several proxies may share
  one login. One owner must serialize refreshes, or they invalidate each
  other's tokens.

The daemon would own the token store (for example the OS keychain), refresh
on the host, and hand current tokens to each session's proxy through the same
control-API path that `vibepit secret refresh` uses. The same daemon would
also replace manual refresh for `command` sources. OpenShell-style refresh
strategies (`oauth2-refresh-token`, `oauth2-client-credentials`) for tokens
the user configures could follow the same path.

## Left to the builder

- CEL vs `expr`, decided by measured binary growth.
- Exact names of new config keys for user-defined presets and the audit
  setting, the new control API endpoint, and new block codes.
- The exact GitHub preset rule lists (verify paths against GitHub's git smart
  HTTP, REST, and LFS docs).
- Leaf certificate cache size and lifetime; CA lifetime matching the existing
  session credentials.
- Where the trust record is stored (session/config dir per `config/paths.go`)
  and exactly which parts are hashed.
- The draft-rules output mechanism (view plus copy, or write to a file).
- Monitor key bindings for the new actions, consistent with `a`/`A`/`N`.
