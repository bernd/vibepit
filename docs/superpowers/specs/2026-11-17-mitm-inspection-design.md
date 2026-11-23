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
outbound Workers, Fly tokenizer, Codex cloud, Claude Code on the web. The
design borrows OpenShell's audit mode, credential/endpoint binding and
placeholder model, Docker's "same allow/deny verbs with optional
method/path", and Vercel's "terminate TLS only where rules need it". From
Claude Code on the web it borrows ref-level push restrictions (its GitHub
proxy rejects branch deletions and non-branch pushes), hosts that never
receive a credential, flagging overlapping secrets at configuration time,
its sandbox CA variable coverage, and an agent-facing troubleshooting file
in the sandbox.

## Goals

- Request-level allow and deny rules (method, path, optional CEL/expr
  condition) on selected hosts.
- Secret injection: tokens live on the host and in proxy memory only; the
  sandbox sees a placeholder.
- Session-wide audit mode plus a monitor action that drafts rules from
  observed requests.
- Named sets and parameterized presets so a list of repos is written once,
  including built-in `github-read` and `github-write` presets.
- Existing configs keep their decisions, apart from the announced breaks
  that section 12 collects for the changelog.

## Non-goals (first version)

- Request-level holds (path refusals are immediate 403s).
- GraphQL matching beyond "query only" (operation names, fields), and MCP or
  other protocol-aware matching.
- Automatic refresh of expired secrets (manual `vibepit secret refresh`
  only; a future host daemon will take this over). Proxy-side OAuth/GitHub
  App refresh. Pushing values again after a proxy restart in `run` mode is
  built; see section 5.
- `bool` preset inputs and conditional (`if:`) rules.
- Presets defined in project config.
- Per-rule or per-preset audit mode.
- Native keychain integration (the `command` source covers it).
- Placeholder replacement inside request bodies.
- Secret scanners, detection of real secret values in requests, body
  inspection, and a scan-wide termination switch. The inspector interface is
  built now; see section 6. The alert for a credential the client sent in a
  service's own header (section 5) looks at one header on the binding hosts
  and is not a scanner.
- Changing the `default` preset (it keeps `vcs-github`).
- Chaining through an upstream (corporate) proxy. The current proxy doesn't
  either; with the new dialer it would be a dialer option.
- Approving credential grants on first use, like a hold; see "Future:
  approving grants on first use". This project uses `vibepit trust`.
- Telemetry exporter configuration in vibepit. `netproxy` provides the
  telemetry APIs now; vibepit keeps exporting off.
- Replacing the config of a running proxy (`ApplyConfig`); see "Future:
  replacing the config at runtime".
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
- The control API listens on the proxy container's addresses, which include
  the sandbox network. mTLS is what keeps the sandbox out: it can reach the
  port but has no client certificate. The endpoint that receives secret
  values sits behind that same listener.
- The sandbox can write everything in the project directory except
  `.vibepit`, including `.git/config`. Nothing on the host may act on the
  project's git configuration.
- In `run` mode the proxy container restarts on its own (`unless-stopped`);
  in daemon mode (`up`) it doesn't (`NoRestart`).
- The host is trusted; the sandbox is not; a cloned repo's project config is
  written by whoever wrote the repo.

## Approaches considered

**When to terminate TLS.**
- *Only where needed (selected):* a `host:port` is terminated when a request
  rule or secret applies to it, or audit mode is on. Every other host stays
  tunneled, so pinned clients and tools with bundled roots keep working
  there.
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
- the host and port are allowed and are a binding host of an active secret
  (section 5), so a placeholder sent there can be stripped;
- audit mode is on and the target is an allowed host on port 443, other than
  `host.vibepit` and the hosts on the audit exclusion list (section 7).
  Other ports may carry non-HTTP protocols that termination would break, so
  in audit mode they stay tunneled and are logged at the connection level.

Otherwise it tunnels as today. Rule hosts with pinned clients are
unsupported and documented as such. Request rules or secrets on
`host.vibepit` are a config error: no public CA certifies it, and upstream
verification has no skip option.

**Policy changes and open connections.** A tunnel is decided once, at
`CONNECT`, and clients keep tunnels open for a long time (HTTP/2,
keep-alive), so a later rule would never reach them. After every policy
change (a decision or a rule add) the proxy runs host admission again,
without holds, for each open connection and closes those whose outcome
became stricter:

- a tunnel whose target would now be refused or terminated;
- a terminated connection whose target would now be refused.

The client reconnects and gets the new decision. Each closure is a flow
record on the connection's kind with its own code. A terminated connection
that stays open needs nothing more: its requests are authorized one by one,
so a new rule applies to the next request. A request already in flight, and
a connection already upgraded (WebSocket), run to their end unless the host
itself is now refused. A change that only loosens policy closes nothing.
Each tunnel keeps the alias chain from its dial, and the re-check matches
the new rules against that chain too: a deny on A also closes a tunnel to
its alias B. No new lookup is made, and CIDR results stay as they were at
the dial.

Upstream connections of terminated and ordinary requests are pooled and
reused without a dial, so the dial checks would not see a new deny either.
A deny therefore replaces the upstream transport: requests in flight finish
on the old one, its idle connections are closed, and the next request dials
and passes the alias and CIDR checks again.

**MITM CA.** A separate per-session CA, not the control-API CA (whose key is
discarded after signing). The CLI generates it with the other session
credentials and delivers its key to the proxy the same way as the control
API server key, so a proxy restart keeps the same CA and the sandbox's trust
stays valid. Leaf certificates use ECDSA P-256 (many clients reject Ed25519
server certs) and are cached per host. Each leaf names only its `CONNECT`
host, gets a random serial, and is valid for a short period well under
browser and OS limits (Chrome rejects leaves valid longer than 398 days, as
goproxy found in #432).

**Sandbox trust.** The CLI builds the sandbox's trust files once, at session
start, and mounts them read-only at fixed paths. Nothing is generated inside
the sandbox. After the proxy container starts, the CLI copies the proxy
image's CA bundle out of it (the roots the proxy itself verifies upstream
certificates with) and writes into the session directory:

- **A PEM bundle** of those roots plus the session CA, mounted over the
  sandbox's system bundle (`/etc/ssl/certs/ca-certificates.crt`; the image
  is Ubuntu). A bind mount works on the read-only root filesystem. Tools
  that read the system store (the system's OpenSSL and GnuTLS, and curl,
  git, Go, and Python's `ssl` on top of them) then trust the session CA
  with no setting at all, including tools that ignore the variables below.
  Tools that bring their own roots still have to be pointed at the file:
  bundled root lists (certifi, Node, Deno) and tools installed with
  Homebrew, whose OpenSSL reads Homebrew's own bundle. So static container
  env still sets `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`,
  `GIT_SSL_CAINFO`, `PIP_CERT`, `CARGO_HTTP_CAINFO`, `AWS_CA_BUNDLE`,
  `DENO_CERT`, `NIX_SSL_CERT_FILE`, `HEX_CACERTS_PATH` (Elixir Hex),
  `BUNDLE_SSL_CA_CERT` (Ruby Bundler), `HTTPLIB2_CA_CERTS` (older Google
  Python clients), `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` (gRPC, which
  reaches terminated hosts over HTTP/2), and
  `CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE` (gcloud), all to that same path; the
  additions follow Claude Code's cloud sandbox. `NODE_EXTRA_CA_CERTS`
  points at the CA alone, mounted as its own file. `NODE_OPTIONS` is not
  set (that sandbox adds `--use-openssl-ca` there):
  `NODE_EXTRA_CA_CERTS` already covers Node, and `NODE_OPTIONS` is often
  set by users and projects, which would replace or be replaced by ours.
  Tool config files (`pip.conf` `cert`, npm `cafile`, `~/.curlrc`, git
  `http.sslCAInfo`) override these variables; the troubleshooting file
  (section 12) tells the agent to point them at the bundle rather than
  turn verification off.
- **A Java truststore** (JKS) with the same certificates. A truststore
  setting replaces the JDK's default store, so it must hold the public roots
  too. `-Djavax.net.ssl.trustStore=<path>` and
  `-Djavax.net.ssl.trustStoreType=JKS` go into the static
  `JAVA_TOOL_OPTIONS`, next to the proxy flags vibepit sets there today.
  Every JVM reads JKS, and the format is simple enough to write with the Go
  standard library, so no JDK, `keytool`, or new dependency is involved. The
  JVM reads the file without a password.

Why on the host and not in the entrypoint:

- Env and mounts are fixed when the container is created, so every way into
  the sandbox gets them: the first shell, a second `run` through an exec,
  daemon-mode shells, and `vibepit exec`. Env set by the entrypoint would
  reach only the shell it starts.
- The files always exist. The Java flags need no "is a JDK installed"
  condition, and a JDK installed during the session (the base image has
  none) works from its first start.
- Maven needs nothing of its own: it runs in the JVM.

What follows from it:

- Tools in the sandbox verify with the proxy image's roots, not with the
  sandbox image's or a JDK's own `cacerts`. A root added to a custom sandbox
  image is no longer in the bundle file, and one imported into a JDK's store
  no longer counts. OpenSSL's hashed directory next to the bundle is left as
  it is, so tools that also search it still find the image's roots there.
  The public roots are equivalent: Debian builds OpenJDK's `cacerts` from
  the system roots the same way.
- The roots are a snapshot from session start.
- The Java flags reach the JVMs that read `JAVA_TOOL_OPTIONS`, as the proxy
  flags do today. GraalVM native images are not among them.
- Spike result (throwaway, not kept in the repo): a JKS file written by Go
  from a system bundle plus a P-256 CA loaded in JDK 21 with all its
  entries, with and without an explicit store type; a leaf signed by that CA
  was accepted through `JAVA_TOOL_OPTIONS`; and a real HTTPS request
  succeeded. Java 8 was not tried.

The CA is installed in every session (rules can arrive at runtime). The CA
never goes into the home volume.

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
HTTP version. A tunnel is a copy loop with half-close, after the server-name
check (below). A terminated
connection is handed to `tls.Server` (leaf from the session CA, ALPN `h2` and
`http/1.1`), then served by a one-connection `http.Server` whose single
handler does request authorization for HTTP/1.1 and HTTP/2 alike, and
forwards with `httputil.ReverseProxy` over a `http.Transport` that uses the
pinned dialer. Ordinary `http://` requests use the same handler path after
host admission.

**Inside a tunnel.** The proxy relays a tunnel's bytes without interpreting
them, with one exception: the start of a TLS handshake, which it follows
until the server has answered.
- **The TLS server name must be the `CONNECT` host.** If the client's first
  bytes are a TLS ClientHello, its server name (SNI) must equal the host the
  tunnel was opened for. The ClientHello travels in clear, so this needs no
  termination. Without the check, one allowed host on a shared frontend
  opens every site on that frontend: most CDNs serve all their customers
  from the same addresses and pick the site by server name, so a client can
  connect to the allowed name and ask for another one in its handshake. That
  gets around path rules on a neighbour, and around the host allowlist
  itself, which is how today's proxy behaves.
- A mismatch closes the tunnel before any client byte reaches the upstream
  and writes a flow record with its own code, naming both hosts. A
  ClientHello without a server name, with more than one name, or with the
  extension twice is refused the same way: a proxy and a server that read
  such a hello differently would disagree about the site.
- IP-literal targets and `host-port` targets are not checked: allowing an
  address or a host port allows whatever it serves.
- **Anything that starts like TLS must be a ClientHello.** The check
  classifies the first bytes by TLS record type, not by "is this a
  handshake". A tunnel whose first record is an alert, a ChangeCipherSpec,
  or application data is refused, and so is an SSLv2-format hello. Passing
  those as "not a handshake" would be a way around the check: a Go TLS
  server drops a warning alert and then accepts the ClientHello that follows
  it, with whatever name it carries (confirmed by experiment; OpenSSL
  refuses the same bytes).
- **The check follows the handshake until the server has answered.** In
  TLS 1.3 a server may answer the first ClientHello with a HelloRetryRequest,
  and the client then sends a second ClientHello, still in clear. A server
  that takes the name from the second one lets a client name the allowed
  host first and another site second (confirmed by experiment against
  OpenSSL's test server; Go's server refuses a changed name). So the proxy
  also reads the server's first handshake message while relaying it. If it
  is a HelloRetryRequest, which its fixed random value shows, the client's
  next handshake record must be a ClientHello that passes the same check
  before it is forwarded; records of other types the client sends before it
  (a compatibility ChangeCipherSpec, early data) are relayed. After an
  ordinary ServerHello no further ClientHello can arrive in clear, and the
  proxy stops reading.
- **Cleartext inside a tunnel is not checked.** A tunnel that carries plain
  HTTP lets the client name the site in its `Host` header and change it on
  every request. The proxy does not refuse it, for two reasons. Node's
  `fetch` sends `CONNECT` for `http://` URLs too (with the
  `NODE_USE_ENV_PROXY` setting vibepit applies), and that is how a local
  model server from `extra-hosts` is reached. And telling HTTP from other
  protocols by its first bytes can be dodged, for example with a leading
  blank line that some servers skip. This matters only where a user allows a
  cleartext port on an address that serves several sites; no built-in preset
  does. The threat model says so.
- First bytes that are not TLS pass as today, since a tunnel also carries
  other protocols (SSH over `CONNECT`). Bytes from the upstream are relayed
  from the beginning, so protocols where the server speaks first keep
  working; only the client's ClientHello records are held until they are
  checked.
- A bounded record reader holds a complete ClientHello across fragments.
  Each hello flight has a 64 KiB wire budget including record headers.
  A single ten-second deadline covers the TLS exchange through the final
  ServerHello, including a retry. Ambiguous legacy SSL prefixes have up to
  ten seconds to supply their framing. The proxy removes these deadlines
  when it starts the ordinary byte relay. Upstream TLS alerts are forwarded
  so clients receive the server's handshake errors.
- On name-checked tunnels, refuse a ClientHello carrying the
  `encrypted_client_hello` extension, including after a HelloRetryRequest.
  The outer name can match the target while the inner name remains hidden.
  This rule also refuses clients sending dummy (GREASE) ECH, even with no
  ECH configuration. Record an ECH-specific refusal so users can distinguish
  it from a name mismatch. Cover real and GREASE ECH in the handshake suite.
  [RFC 9849, sections 6.1 and 6.2](https://www.rfc-editor.org/rfc/rfc9849.html#section-6)
- **Built early, against real servers.** Three review rounds each found a
  way past this check on paper, because it reads a protocol the proxy
  doesn't terminate and servers differ in what they accept. It is built
  right after the first acceptance test (section 13), with a harness that
  runs every known case against OpenSSL, Go, and Node servers. A new case
  found later is added to that harness first.

**Inside a terminated connection.**
- The request's authority must equal the `CONNECT` target (default port
  applied): `r.Host` (the `Host` header or HTTP/2 `:authority`) and, for an
  absolute-form request, the URL's host and scheme `https`. A mismatch gets a
  `421 Misdirected Request`, never followed. 421 is also what tells an
  HTTP/2 client that tries to reuse ("coalesce") the connection for another
  host to open a new one. For HTTP/1.1 the first implementation requires
  origin form even if an absolute URI names the correct target (400).
  Go drops the wire Host field while parsing an absolute URI, so the
  handler cannot independently verify both authorities. Ordinary HTTP
  proxy requests still use absolute form. goproxy serves coalesced requests
  (`TestMitmHTTP2Coalescing`), the new proxy deliberately does not.
- The client must speak TLS. Only `tls.Server` reads a terminated connection,
  so cleartext and h2c are never served; a non-TLS first byte closes the
  connection.
- A failed handshake with the client is recorded. A client that doesn't
  trust the session CA (pinned certificates, bundled roots) ends the
  handshake with an alert or just closes. Either way the proxy writes a flow
  record on the `http-connect` with its own code and, when the client sent
  one, the alert in the reason. The monitor then shows which hosts can't be
  terminated, where otherwise the tool would only fail.
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
  (through the resolver the embedder supplies, section 11; the proxy
  already queries upstream DNS directly for aliases), checks the
  chain against deny rules and every address against the CIDR rules, and
  dials one of those addresses, keeping the hostname for TLS verification.
  This applies to tunnels and ordinary requests too. Addresses are IPv4
  only for now, as today's alias query is; IPv6 would add a second lookup
  whose chain has to be checked as well.
- **Names from `extra-hosts` come from a static table, not from DNS.** Today
  they reach the proxy through its container's hosts file, which
  `net.Resolver` reads and a resolver that queries a DNS server directly
  does not. vibepit passes them to `netproxy` as a name-to-address table
  that the dialer consults before the resolver. The CIDR check applies to
  those addresses as it does today, and a static entry has no alias chain.
  An entry whose value is `host-gateway` gets the gateway's address, resolved
  as below. An IPv6 value is a startup error for now.
- **The alias check covers request-level denies, for tunnels.** A name in
  the chain that a host-level deny matches refuses the dial, as today. A
  blind tunnel leaves a second way around a deny: through a tunnel to an
  allowed name B that aliases A, the client can ask A's server for A's pages
  (its own TLS, `Host: A`), and a path deny on A never sees the request. So
  a tunnel's dial is also refused when a name in the chain matches the host
  and port of a request-level deny. The refusal names A and says how to
  reach B: give B a request rule of its own, which terminates it.
  Terminated connections and ordinary `http://` requests need no such
  check. Their authority is pinned to the name the client connected to (for
  an ordinary request the URL's host overrides the `Host` header), so A's
  pages can't be asked for through B. The check never applies A's path
  rules to B's requests, and it never terminates a host because of what DNS
  returned: whether a host is terminated depends on the config alone.
- **The one exception is the authorized `host.vibepit` rewrite.** When host
  admission rewrites `host.vibepit` on an allowed port to the configured
  gateway, the dialer connects to exactly that gateway address without the
  CIDR check, as today. Any other name that resolves to the same private
  address stays blocked. Today the gateway is a name (`host-gateway`) that
  the proxy container's hosts file resolves; vibepit resolves it once when
  the proxy starts and gives `netproxy` the address.
- **A refused dial is reported on the flow that needed it,** with today's
  codes (alias of a denied name, blocked CIDR range, resolution failed);
  `AllowFixes` is false. The dialer returns a typed error and its caller
  maps it:
  - *Tunnel:* the proxy dials before it answers, so the `CONNECT` gets a 403
    with a body, and the `http-connect` (or `host-port`) record carries the
    block. Today a refused `CONNECT` is closed without a response; every
    refusal at `CONNECT` now answers 403.
  - *Request* (ordinary `http://`, or inside a terminated connection, whose
    `CONNECT` was answered before any dial): the request gets a 403 with the
    block body, and its `http-request` record carries the code, with the
    connection as `parent`. This also covers a reconnect late in a
    long-lived terminated connection.

  An upstream that can't be reached (connection refused, timeout, bad
  certificate) is a 502, not a block.
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
- Request rules bind a name, not a server. A rule on A restricts the
  requests the proxy sends to A. Two ways to A's server through a tunnel to
  another allowed name are closed in section 1: an alias of A (the alias
  check) and a handshake that names A (the server-name check). One remains.
  A client can tunnel to an allowed name X honestly and then ask for A in
  the encrypted request. Servers accept that when X's certificate also
  covers A, which is how HTTP/2 connection reuse works, and a tunnel can't
  see it. No credential can travel that way, since injection needs a
  terminated connection. A host that needs path rules should still not have
  a tunneled neighbour under the same certificate; the built-in presets
  follow that (section 4), and the threat model says so.

**Path matching.**
- `*` matches any run of characters inside one segment, never a `/`: alone
  it matches a whole segment, and `*.git` matches `api.git`. `**` matches
  zero or more whole segments (`/repos/x/**` covers `/repos/x`) and stands
  alone in its segment. No `?` or character classes.
- Patterns are written in decoded form and matched against the decoded
  path; a pattern's own text is never percent-decoded. There is no escape. A
  path whose decoded form contains `*`, `{`, or `}` can't be written as a
  literal pattern in this version, and nothing that turns an observed path
  into a rule may treat one as a literal (section 9).
- A single trailing slash is ignored on both the pattern and the path:
  `/repos/*` matches `/repos/x/`, and a deny on `/admin` matches `/admin/`.
  Servers commonly treat the two as the same page, so a deny has to cover
  both, and for an allow the path stays under the same prefix. `request.path`
  in a `when` condition has it removed as well (the root stays `/`). The
  original bytes are forwarded, and `//` is still rejected.
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
- The first acceptance milestone uses `expr` for typed boolean conditions on
  `request.path`. A stripped, CGO-disabled Linux arm64 HTTP binary built with
  Go 1.27.4 grew by 3.41 MB with expr v1.18.1 and 8.78 MB with cel-go v0.32.0
  when compiling and evaluating a condition. Before exposing config, verify
  the rest of the documented expression environment and the collection
  examples against the selected engine.
- Environment: `request.method`, `request.host`, `request.port`,
  `request.path`, `request.param(name)`, `request.header(name)`,
  `request.json` (request body parsed as JSON, only when the expression
  references it), `request.graphql.queryOnly`, `request.git.push`, and
  `request.git.updates`.
- A condition that references `request.path` is a path decision and follows
  the path rules above, even on a rule without `paths`: the request is
  rejected if either interpretation contains `.`, `..`, `//`, or NUL, and the
  condition is evaluated once per interpretation. An allow matches only if it
  is true for every interpretation; a deny matches if it is true for any.
- `request.param(name)` and `request.header(name)` must be unambiguous.
  Upstreams disagree on which value they use when a name is repeated (the
  first, the last, or all joined). A request is ambiguous for a condition
  when a parameter or header the condition references appears more than
  once, or when the condition references a parameter and the query string
  doesn't parse cleanly (for example `;` separators or invalid escapes). An
  ambiguous request counts as an evaluation error: an allow rule does not
  match; a deny rule matches.
- Header conditions read the outbound end-to-end headers after the proxy's
  hop-by-hop and forwarding-header cleanup. A reference to a header removed,
  rewritten, or synthesized by the proxy or its transport is an evaluation
  error. This includes service credential headers on active binding origins.
  Use an explicit error for those names even when the inbound header is
  absent. The policy never needs a real credential to evaluate a condition.
  Inspectors still receive the original client headers. Test a condition on
  a removed header and one on a preserved header against what the upstream
  actually receives.
- `request.json` is parsed strictly, for the same reason: parsers disagree
  on duplicate keys (Go's `encoding/json` takes the last one). A body with a
  duplicate key in any object, invalid UTF-8, or data after the JSON value
  is unparseable. `request.graphql` reads the `query` field from this strict
  parse.
- `request.graphql.queryOnly` is true only when the body is a single GraphQL
  request (not a batched JSON array) whose document, parsed with a real
  GraphQL parser (`gqlparser`), contains only query operations. A text check
  is not acceptable: comments or a fragment before `mutation` would get past
  it. Parsed only when an expression references it.
- `request.git` describes a git smart-HTTP push, so rules can limit which
  refs a push changes, not just which repository it goes to:
  - `request.git.push` is true for a `POST` whose path ends in
    `/git-receive-pack` (in every path interpretation), whatever its
    `Content-Type`. The git server parses such a body as a push whatever
    the header says, so the proxy does too.
  - `request.git.updates` is the push's list of ref updates, each with
    `ref`, `old`, and `new` (hex object IDs) and the derived `create` (`old`
    is all zeros) and `delete` (`new` is all zeros). It comes from the
    pkt-line command list at the start of the body, which ends at the first
    flush packet. Capabilities after the NUL on the first line are ignored.
    Only the command list is buffered (within the 64 KiB cap below); the
    pack data after it streams upstream unbuffered.
  - A gzip `Content-Encoding` is decoded before parsing; any other encoding,
    a malformed pkt-line, a command list without its flush packet within the
    cap, a short or non-hex object ID, or a ref name that `git
    check-ref-format` rejects (`..`, `//`, control characters, `@{`, a
    `.lock` suffix) is a parse error.
  - On a request where `request.git.push` is false, referencing
    `request.git.updates` is an evaluation error. A push rule's condition
    therefore belongs on the rule for the receive-pack path; CEL's `||`
    absorbs the error in `!request.git.push || ...`.
  - The whole request gets one decision. Write allows with `all` and denies
    with `exists`, for example
    `request.git.updates.all(u, !u.delete && u.ref.startsWith("refs/heads/"))`
    to allow only creating and updating branches, or a deny with
    `request.git.updates.exists(u, u.ref == "refs/heads/main")` to keep
    the agent off `main`.
  - Force pushes can't be told apart from ordinary ones: whether an update
    fast-forwards depends on history the proxy doesn't have, and `old` is
    only what the client says the ref was. Branch protection on the server
    stays the control for force pushes, and the docs say so.
  - Git has no protocol v2 for push, so the command list format above is
    the only one to parse.
- Bodies are buffered only for rules that reference `request.json`,
  `request.graphql`, or `request.git`, up to
  64 KiB. Body too large, unparseable, or an evaluation error: an allow rule
  does not match; a deny rule matches. A push that updates so many refs
  that its command list passes the cap (a large `git push --mirror`)
  doesn't match a ref-limited allow; pushing fewer refs at a time works.
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

**Using presets** (global or project config; `secret` in a project config
needs project trust, see section 5). Strings stay valid:

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
  replaces: [vcs-github, cdn-github]
  allow-http:
    - host: github.com:443
      methods: [GET]
      paths: ["/{repos}.git/info/refs", "/{repos}/info/refs"]
      credentials: true
    - host: avatars.githubusercontent.com:443
      methods: read-only
```

- Input type: `list` only (substituted into paths like a set; accepts an
  inline list or a set name). Hosts in preset rules are literal. A `string`
  type (for example a GitHub Enterprise host) can be added later without
  breaking existing presets.
- An input without `default` is required. Unknown inputs, wrong types, and
  missing required inputs are startup errors. An empty list is allowed and
  expands to no patterns.
- `secret` is accepted by every preset and applies only to rules marked
  `credentials: true`. If the secret's service doesn't cover a credential
  rule's host and paths, that's a startup error.
- `replaces` removes the named preset wherever it appears, including inside
  `default`. A replacing preset must include entries for the hosts it does
  not narrow.
- **A narrowing preset leaves no host of its provider tunneled.** A tunneled
  host under the same certificate as a restricted one would let a client ask
  for the restricted host inside the tunnel (section 2). So every host in a
  preset that restricts a provider by path gets a request rule and is
  terminated: a real restriction where one fits (`methods: read-only` for
  `avatars.githubusercontent.com` above), otherwise `paths: ["/**"]`. The
  docs give the same advice for user-defined presets.
- **That holds for the hosts other presets bring in, too.** `default` also
  includes `cdn-github`, which allows `ghcr.io` and
  `pkg-containers.githubusercontent.com` as whole hosts, a tunneled
  neighbour of the `githubusercontent.com` hosts that `github-read`
  restricts. The GitHub presets therefore replace `cdn-github` as well and
  carry its two hosts with rules. Homebrew downloads its bottles from
  `ghcr.io`; that this still works through a terminated connection has to
  be tested before the presets ship.
- **A startup warning catches the rest.** A check per preset can't see what
  another preset or the user's own entries allow. So vibepit warns at
  startup, and the monitor shows, when a whole-host entry sits next to a
  host with request rules under the same parent domain (one name is the
  other's parent, or both share a parent of at least two labels). It is a
  hint, not a proof: the two may not share a certificate.
- Existing `domains:` and `includes:` keep working.
- The GitHub presets assume git over HTTPS. Outbound SSH (for example
  `github.com:22` through a `CONNECT`-based `ProxyCommand`) can't be
  inspected, is always tunneled, and would bypass any repo, method, or ref
  narrowing. The image already rewrites GitHub SSH remotes to HTTPS:
  `image/config/gitconfig`, installed as `/etc/gitconfig`, maps
  `git@github.com:` and `ssh://git@github.com/` to `https://github.com/`, so
  cloned repos with SSH remotes go through inspection unchanged (Claude
  Code's cloud sandbox does the same through `GIT_CONFIG_*` variables). A
  user or repo `insteadOf`/`pushInsteadOf` back to SSH, or a
  `GIT_SSH_COMMAND`, still needs `:22`, which stays not allowed; the preset
  docs say not to allow it.
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
    `/graphql`. Like Claude Code on the web's GitHub proxy, it only creates
    and updates branches: its push rule has
    `when: request.git.updates.all(u, !u.delete && u.ref.startsWith("refs/heads/"))`,
    so ref deletions and pushes to tags, notes, or any other ref namespace
    get a 403. Its REST rules hold the same line: no `DELETE` on
    `/repos/{repos}/git/refs/**`, creating refs (`POST
    /repos/{repos}/git/refs`) only when `request.json.ref` is under
    `refs/heads/`, updating refs only under `heads/` and without
    `request.json.force`, and no release or tag endpoints. Deleting
    branches, pushing tags, or protecting `main` from direct pushes is the
    user's choice, through an object-form allow or deny with its own `when`
    (examples in section 2) or a user-defined preset.
  - `github-graphql-read`: `POST /graphql` with credentials and
    `when: request.graphql.queryOnly`, so `gh` works for reads.
  - `github-graphql-write`: `POST /graphql` with credentials, mutations
    included.
- Only `github-read` and `github-write` carry `replaces` and the hosts of the
  presets they replace. The GraphQL presets add one rule on `api.github.com`
  and nothing else; they are used next to `github-read`, `github-write`, or
  `default`. With `default`, `api.github.com` keeps its whole-host entry, so
  the GraphQL rule restricts nothing there and only decides where the
  credential goes.
- `repos` stays an honest limit: every rule of a `github-read`/`github-write`
  instance that carries a credential or reaches repository content is
  scoped by it. Rules for hosts without repository paths (avatars, the
  website's assets) are not. GraphQL serves every repository through
  one endpoint, so it can't be scoped by `repos`; the GraphQL presets take no
  `repos` input and reach everything the token can. Their descriptions and
  the docs say so ("all repositories this token can access"). GitHub
  requires authentication for GraphQL, so both presets
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
entry type alongside without changing existing definitions. Everything
around injection is shared by all kinds: sources, delivery, placeholders and
their detection, redaction, and binding to the targets the definition
allows. Only applying
the credential to a flow is per kind.

`auth` styles for `http-request`: `bearer`, `token`, `{basic: <user>}`,
`raw`.

Each `http-request` inject entry binds to an **HTTPS origin**: scheme
`https`, the listed hosts, and an explicit port (`port`, default 443). A rule
with `secret` whose port differs from every binding is a config error. An
inject entry without `kind`, or with a kind that isn't injectable, is a
config error.

**Hosts in a binding are literal.** Every entry in an inject entry's
`hosts` is a full DNS name. A `*` anywhere in it, an IP address, or a
trailing dot is a config error, in built-in and inline services alike. A
wildcard binding on a domain shared by many tenants (`*.amazonaws.com`,
`*.githubusercontent.com`, `*.vercel.app`) would send the token to any
tenant's server. Claude Code on the web accepts `*.` on shared-environment
secrets; vibepit doesn't. A rule with `secret` may still use a wildcard
host, since rules can't widen a binding: the credential is only attached
on the binding's literal hosts.

**Hosts that never take a credential.** vibepit keeps a built-in list of
public package registries (`registry.npmjs.org`, `registry.yarnpkg.com`,
`pypi.org`, `files.pythonhosted.org`, `index.crates.io`,
`static.crates.io`, `proxy.golang.org`, `sum.golang.org`, `jsr.io`,
`npm.jsr.io`, `rubygems.org`, and the like; Claude Code on the web keeps a
similar list). An inject entry bound to one of them is a config error
naming the host, unless that entry sets `public-registry: true`. These hosts
serve anonymous downloads in bulk, so a stray token adds exposure on
high-volume traffic and can break public fetches (a registry that rejects
an unknown token fails the request rather than ignoring the header). The
opt-in covers real use, such as private npm packages on
`registry.npmjs.org`; GitHub's own registries (`npm.pkg.github.com`) are
not on the list. The list is vibepit policy, passed to `netproxy` as data
like the service definitions.

**Secrets** (values, sources, and inline service definitions) are
configured in global config only. Rules that *use* a secret may also come
from a project config, after project trust (below):

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
- At startup the CLI saves the active secret names and their approved source
  descriptors (environment variable names or commands) in a session manifest
  readable only by the host user, outside both container mounts. It stores
  no resolved values. Source commands run from a host-controlled working
  directory outside the project. Startup, automatic re-delivery, and
  `vibepit secret refresh` use those descriptors. A fresh refresh process
  resolves the current value of the session's source and pushes over that
  session's mTLS connection. Renaming or replacing a source in global config
  takes effect on the next session. A missing or unreadable manifest fails
  refresh. Test refresh from a second CLI process after changing the global
  source under the same name.
- After a proxy restart no values are loaded. Requests that need a secret
  get a 403: "secret `github` not loaded, run `vibepit secret refresh`".
  Everything that needs only the placeholder keeps working (below).
- In `run` mode, the only mode where the proxy restarts on its own, the
  attached CLI brings the values back. When the session has secrets it
  follows the stream's `status` topic, which doesn't enable holds; when it
  sees a new instance ID it resolves the sources again and pushes.
  `vibepit secret refresh` stays as the fallback: if the push fails, the 403
  above and "secrets missing" in the status section say so.

**Placeholders.** One random placeholder per secret per session (for example
`vibepit-secret-github-<random>`), stable for the session, set in the sandbox
env for each name in the service's `env`. It exists so tools like `gh` run;
it carries no value. A script that reads the variable directly gets the
placeholder, not a usable token; the docs say so, as Claude Code on the
web's do for its fixed `proxy-injected` placeholder.
- **Only for active secrets.** A secret is *active* in a session when the
  session's config has a grant for it, or when audit mode is on. An active
  secret gets a placeholder, the env gets it, and every allowed binding host
  of the secret is terminated (section 1), whether a grant names that host
  or not. Both parts are needed. Without the placeholder a tool has nothing
  to send, and without termination on all binding hosts the placeholder
  would travel through a tunnel as if it were a token: a grant on
  `api.github.com` alone would leave git sending it to a tunneled
  `github.com` and getting a 401.
- A secret that is configured globally but not active leaves the env alone
  and terminates nothing, so a login the tool has in the home volume keeps
  working. Audit mode activates every configured secret so that an
  authenticated flow can be observed before its first grant exists; a secret
  with a binding host on the audit exclusion list is not activated by audit
  mode alone. Grants don't appear at runtime (the CLI and the monitor never
  write `secret`), so what is decided at session start holds for the
  session.
- **The CLI generates the placeholders at session start and writes them into
  the session's proxy config,** next to the secret names and service
  definitions. A placeholder is not a secret (the sandbox env already has
  it), so this stores no secret value at rest. Placeholders never travel
  with the values.
- **The proxy knows them from its first request after any restart.**
  Stripping the service's own header, the placeholder inspector, and
  redaction in flow records, spans, and logs work without loaded values;
  only injection waits for them. If placeholders arrived with the values, a
  restarted proxy would forward the placeholder upstream as a credential (a
  401 where an anonymous request used to succeed), miss placeholders sent
  elsewhere, and write them into flow records.
- **One literal match finds a placeholder.** It uses only URL-unreserved
  characters (letters, digits, `-`, `.`, `_`, `~`), so it looks the same in
  a header, a path, and a query; only Basic auth needs decoding first.
  Secret names are limited to the same characters.

Some clients check a token's shape before sending it (a prefix, a length,
a character set). A service definition may set `placeholder`, a template
with literal parts and exactly one `{random:N}`, for example
`placeholder: "ghp_{random:36}"`:
- Literal parts use only `A-Z`, `a-z`, `0-9`, `_`, `.`, and `-`; the random
  part uses `A-Z`, `a-z`, and `0-9`, and `N` is at least 24. Anything else
  is a config error. The random part keeps each placeholder unique per
  session, so detection, stripping, and redaction work exactly as with the
  default form.
- Without `placeholder`, the default is
  `vibepit-secret-<name>-{random:32}`. Built-in services set a template
  only where a known client rejects the default.
- A realistic-looking placeholder can trip secret scanners (including
  GitHub push protection) if the agent commits it. That is accepted: it is
  worthless, and a scanner hit on it is a useful signal.

**Injection.** For a request inside a terminated `CONNECT` connection that
passes request authorization (the only request form that can carry a
credential). A secret's *binding hosts* are the host and port of its
`inject` entries; the rules below are scoped by them.
- If an allow rule with `secret: X` matches, the request is allowed overall
  (no deny matches), and the host and path fall within one of X's `inject`
  entries, the proxy sets that entry's header to the formatted value. It
  does so whether the agent sent the header or not, and replaces every value
  the agent sent for it (placeholder, an empty value, or a token of its
  own). Git and many SDKs send no `Authorization` until a server answers
  401, so the proxy can't wait for one: with the header set on the first
  request, the server never challenges, and git needs no credential helper
  or askpass in the sandbox. Within a binding the agent can't use a
  credential of its own instead; that's the binding's purpose.
- Immediately before attaching, the proxy checks the final outbound URL:
  scheme `https` and authority equal to the binding's host and port. Plain
  `http://` requests, cleartext inside `CONNECT`, and any other port never
  carry a credential upstream.
- If two different secrets would set the same header on one request, block
  it and log the conflict. (Claude Code on the web sends one of them; a
  silent choice between credentials is not acceptable here.)
- Overlaps are also reported before any request, at config load: two
  secrets that both have grants, whose inject entries share an origin and
  header name, and whose path patterns (narrowed by the grants' paths) can
  match a common path. The check is conservative: patterns count as
  overlapping unless segment-by-segment comparison proves them disjoint,
  and `when` conditions are ignored. Each overlap is a startup warning
  naming both secrets, the origin, the header, and the patterns. It also
  appears in the monitor's status and, when a project grant is involved,
  in the `vibepit trust` list. A warning, not an error: the runtime block
  already keeps it safe, and an error would stop sessions over grants that
  may never be used together.
- Otherwise, if the request goes to one of the secret's binding hosts and
  the entry's header carries the placeholder (decoding Basic auth), strip
  the header and forward unauthenticated. The flow record is
  marked "secret withheld" with the secret's name: the client tried to use
  the secret and no grant matched. Without the marker the user only sees
  the upstream's 401 or 404.
- **A credential the client sent itself is reported.** When a request to a
  binding host carries, in that entry's header, a value that is neither
  absent nor the placeholder, a real credential for that service is in the
  sandbox, for example a token from an earlier login in the shared home
  volume. A header of the same name on any other host is not looked at: the
  agent's own API key for another service raises nothing. Neither does a
  secret that is not active in the session, where a login in the sandbox is
  the intended way. For an active secret, a credential in the sandbox is
  what secrets are meant to end, so it must not pass silently. If a grant matches, the header is overwritten as above:
  a matching grant decides the credential. If none matches, the header is
  forwarded unchanged, as today. Either way the proxy raises an alert
  finding (not a block) that doesn't contain the value.
- A placeholder found anywhere else blocks the request and raises an alert
  in the monitor: in another header, the path, or the query of any request
  the proxy reads, or in any header at all, the service's own header name
  included, on a host outside the secret's binding hosts. This covers
  ordinary `http://` requests as well. Such a request is always outside a
  binding, which is an HTTPS origin, so a placeholder in it is blocked, not
  stripped. Bodies are not scanned. Placeholders sent to tunneled hosts are invisible; they are
  worthless, so this is a detection signal, not a protection. This check is
  implemented as the first request inspector (section 6); stripping the
  service's own header stays part of injection.

**Where credential grants come from.** A credential grant is an allow rule
or preset use with `secret`.
- **Grants in the global config apply to every project the user has
  approved** (below). That is their definition, and the docs say so plainly:
  a grant for private repos in the global config can be used from any
  approved checkout.
- **Per-project grants go in the project config** (for example a project's
  own list of private repos). Secret values, sources, and service
  definitions never come from a project config.

**Project trust (`vibepit trust`).** A project config is written by whoever
wrote the repo, and it decides where the sandbox may connect: hosts, DNS
names, presets, ports on the user's own machine (`allow-host-ports`), and
credential grants. The home volume, with the agents' logins in it, is shared
by every project, and global credential grants work in every project, so
the allow list of a freshly cloned repo is enough to send data out. vibepit
therefore uses no project config the user hasn't approved, the way
`direnv allow` and `mise trust` work. This covers the whole file, not only
credential grants.
- **What is approved.** Two things, both bound to the project's resolved
  root path and stored on the host outside the repo:
  1. *The project config,* as a hash of its parsed, canonical form, so
     comments, order, and whitespace don't matter. Any other edit asks
     again.
  2. *The effective credential policy.* vibepit expands the merged config
     (global plus project, sets and presets resolved, built-in presets
     included) into a canonical list of credential grants: per secret, the
     HTTPS origin, methods, path patterns, `when` conditions, and every deny
     on that origin. It is hashed for the grants the project contributes to
     (its own grants, or entries it adds to a set that a grant references).
     This part can change without a project edit, and then asks again: a
     vibepit upgrade whose built-in preset widens a grant, or a global edit
     that changes a grant the project contributes to.
- **vibepit's own writes keep the approval.** First-time setup,
  `--reconfigure`, and rules saved from the CLI, monitor, or prompt are the
  user's decisions on the host. Under the project config lock, vibepit
  checks that the file it read is the approved one (or doesn't exist yet),
  writes, and records the new approval. If something else changed the file
  since it was approved, the write still happens but the approval is not
  renewed, and the next start asks.
- **What the user sees.** `vibepit trust`, run in the project directory,
  lists what the project config adds beyond the global config: allowed
  hosts and DNS names, presets, host ports, and credential grants per
  secret. Grants that paths can't limit (the GraphQL presets) are flagged as
  "all repositories this token can access". It also lists the global grants
  that become usable in this project. Then it asks for approval.
  `vibepit trust --revoke` removes the approval.
- **Where it's checked.** Where the project config is loaded: when `run`
  starts a new session it shows the same list and asks if there is no
  approval or a hash changed; `up` refuses with a hint to run
  `vibepit trust`. A project without a config goes through first-time
  setup, which approves what the user just chose. `connect`, and `run`
  attaching to a running session, don't load config and don't ask.
- **The project root is found without git.** The approval is bound to the
  root path, and the root is what gets mounted, so the sandbox must not be
  able to move it. It can write the project's `.git/config`, and
  `git rev-parse --show-toplevel` obeys a `core.worktree` set there: the
  lookup took a parent directory as the root and the next session mounted
  it read-write.
  The host integration depends on `fix/project-root-lookup`, which is
  separate from the inspection branch. Use its physical root for approval,
  session lookup and the project mount. It walks upward looking for a root
  recorded in host state or an owned `.git` marker, and stops at a foreign
  marker. Root records establish identity. Config and grant hashes still
  decide whether the user has approved the current policy.
- **The prompt names what will be mounted.** The trust prompt and first-time
  setup show the root path. If that root contains a project that is already
  approved, or lies inside one, the prompt says so and names it. A root
  above a known project is either a deliberate outer project or a sign that
  the root moved. A root inside one can be planted: the sandbox can create
  `sub/.git` or `sub/.vibepit/network.yaml`, and a session started in `sub`
  would then run without the approved project's config.
- **Declining** aborts startup with a message naming what was not approved.
  There is no mode that starts without the project config.
- **The agent can't change the project config mid-session:** `.vibepit` is
  hidden in the sandbox behind a read-only mount, and config files are read
  once, at session start (section 11).
- **Upgrade.** Every existing project asks once after the upgrade (vibepit
  is 0.x); the changelog says so.

### 6. Request inspectors

An extension point for detecting exfiltration attempts, so secret scanners
can be added later. This project builds the interface and its place in the
request path; the placeholder check is the only inspector. Injection raises
one finding of the same type itself, for a credential the client sent
(section 5): it knows which grant and header are in play, which an
inspector running before it does not. That finding carries no prefix or
hash of the value.

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
- Terminates TLS on every allowed host on port 443 (section 1) and logs
  every request with method and path.
- Requests refused only for not matching a path allow are let through and
  logged as "would block".
- Not relaxed: the host allowlist (unlisted hosts are still blocked or held),
  host-level denies, explicit path denies, and inspector blocks (such as a
  placeholder sent outside its binding).
- Audit mode lives in the policy, not in an inspector: it relaxes a decision,
  and inspectors may only add alerts or blocks, never turn a block into an
  allow. What it shares with future scanning is the broad termination scope
  (section 6).
- Secret injection works as in normal mode. A "would block" request matched
  no grant, so it goes out without a credential: audit mode must not widen a
  grant to the service's whole binding. Its flow record carries the "secret
  withheld" marker (section 5), the monitor shows it next to "would block",
  and the draft-rules view notes for such entries that the drafted rule
  needs `secret:` to work. Drafting rules for an authenticated flow can
  therefore take more than one run. Audit mode makes every configured secret
  active (section 5), so the placeholder is there to be sent and withheld
  even before a first grant exists.
- **Excluding hosts.** Audit mode terminates hosts that no rule asked to
  terminate, so a pinned client on an allowed host stops working while it is
  on. A host-controlled list (`audit-exclude` in the global config and a
  matching flag; host patterns as in `allow-http`) takes hosts out of audit
  mode: they are not terminated because of it and their path allows are not
  relaxed. A host that request rules or a secret already terminate stays
  terminated. Excluded hosts are logged at the connection level, like other
  tunnels. In a project config the list is a config error, like `audit`.
- The monitor marks failed client handshakes (section 1) and, in audit
  mode, names the exclusion list as the fix.
- Monitor "draft rules" action: from selected request entries, generate
  object-form `allow-http` YAML grouped by host and method with path
  patterns, for the user to edit into a preset. Entries whose path contains
  pattern syntax are left out of the draft and listed separately
  (section 9).

### 8. Logging and events

- Request-level records gain method, path, query parameter names, status,
  mode (tunnel/mitm), secret injected or withheld (name only), and an audit
  marker. The path is recorded as it was sent, percent-encoding included.
  On the wire they are the details of `http-request` flows (see "A generic
  protocol" below).
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
  misuse, missing secret values, failed client handshakes, server-name
  mismatches in tunnels, and connections closed by a policy change. None of them can be fixed by allowing the target. A
  request-level "not allowlisted" can be fixed, but only by a rule with a
  method and a path (below), never by allowing its target.
- `http-request` flows go into their own ring buffer (10k), so floods don't
  push out host-level blocks. `/events` carries both.
- Target rows count flows per kind (connections for `http-connect`, requests
  for `http-request`, queries for `dns-query`, connections for `host-port`).
  Only a connection's first record counts; records that name a `parent` are
  later events on a connection already counted.

**A generic protocol: kinds, targets, flows.** The control API and event
stream are not written per protocol, so future network use cases don't need
a new stream or new endpoints. Today's model is halfway there (`Source` is
`proxy` or `dns`, `HostKey` is a target), but event types, the host table,
holds, and endpoints such as `/allow-http` and `/allow-dns` are each
protocol-specific.

- **Kind.** Every network use case is a kind. This project has four:
  - `http-connect`: a `CONNECT`, or the host admission of an ordinary
    `http://` request; target `{host, port}`.
  - `http-request`: a request the proxy authorizes, inside a terminated
    connection (`mode: mitm`) or as an ordinary `http://` request
    (`mode: plain`); target `{host, port}`, details method, path, query
    parameter names, status. Path refusals, inspector findings, audit
    "would block" records, and the monitor's path actions work the same for
    both modes; only `mitm` requests can carry a credential.
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
  `{id, kind, target, decision, code, reason, rule, detail, ...}`. It
  replaces `LogEntry` on the wire; the fields from section 8 above (method,
  path, status, mode, secret, audit marker) live in the `http-request`
  details. The `id` is unique within one proxy instance.
- **One record per flow, written when its outcome is known.** A refusal is
  written at once. A forwarded request is written when the response status
  arrives or the upstream fails, not when the body ends, so a long download
  or an event stream shows up while it runs. A connection gets one
  `http-connect` record when its admission is decided: for a terminated
  connection that is at `CONNECT`, before any dial; for a tunnel it is after
  the dial, so a refused dial is that record's block. Later events on the
  connection (a failed client handshake, a server-name mismatch, a closure
  after a policy change) are records of their own that name it as `parent`.
- **Rule identity.** Every rule in `netproxy`'s config has an ID that
  `netproxy` assigns and an opaque `origin` label that the embedder sets and
  `netproxy` never interprets. vibepit uses `global`, `project`,
  `preset:<name>`, `cli`, and `runtime` (decisions made during the session).
  A flow record's `rule` is the ID of the rule that decided it; the policy
  in the snapshot and `policy` events list each rule with its ID, canonical
  form, and origin, so a client shows "allowed by preset github-read" by
  looking it up. An index into the expanded rule list would mean nothing to
  a user, since sets and presets expand into many rules. IDs are stable for
  one proxy instance.
- **Parent flows.** A flow record has an optional `parent`: the ID of the
  flow it belongs to. An `http-request` names the admission record of its
  connection (the `http-connect` of the terminated connection, or of the
  ordinary `http://` request), so the monitor can group requests under
  their connection. Later kinds use the same field (an MCP call inside an
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
  (methods, paths, `when`) from the CLI and from the monitor's path actions
  go through a rules endpoint that takes a kind and a rule.
- **`http-request` targets are not allowable.** Their target is
  `{host, port}`, so allowing one through `/decisions` could only produce a
  whole-host rule, which would remove the very path restriction that
  refused the request. The proxy marks them `allowable: false`, and
  `/decisions` refuses them for every action: a deny or reject of a whole
  host is made on its `http-connect` target. A request is allowed or denied
  by a rule with its method and path, through the rules endpoint.
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
- **HTTP rules never apply to `host.vibepit`.** Host admission routes the
  rewrite name to the `host-port` kind before any HTTP rule is looked at, so
  only `allow-host-ports` and `deny-host-ports` decide it. Broad patterns
  such as `*.*:22` stay legal and simply don't cover it, in either
  direction. This is a breaking change (vibepit is 0.x): today HTTP rules
  are decided before the gateway allowance (`proxy/http.go`), an
  `allow-http: host.vibepit:3000` entry opens the port, and that entry is
  what allowing from the prompt saves.
- Two cases are startup errors, so that nothing changes meaning silently.
  Each names the entry and prints the `allow-host-ports`/`deny-host-ports`
  lines to write instead:
  - an `allow-http` or `deny-http` entry whose host pattern ends in the
    `vibepit` label (`host.vibepit:3000`, `*.vibepit:*`): it can only mean
    the host;
  - a `deny-http` entry that matches `host.vibepit:P` for a port P in
    `allow-host-ports` (for example `*.*:3000` next to
    `allow-host-ports: [3000]`): today that deny wins, and dropping it would
    open the port.
- A wildcard allow that used to cover `host.vibepit` (such as `*.*:*`) no
  longer grants host ports. That only narrows; the changelog says so.
- Accepted risk: `deny-host-ports` is a new key, and an older binary ignores
  it while still honouring `allow-host-ports`. That re-opens a port only
  when the same config also allows it; otherwise the older binary merely
  asks about the port again. Unlike a dropped `deny-http`, it takes a
  contradictory config, so the new key stays.
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
   including runtime allows and denies, and the status section (below).
   Every stream, resumed or not, starts with that state at sequence N, then
   sends only changes on the same sequence. The snapshot holds only the
   sections the subscription's topics cover: a `holds,policy` subscriber
   gets open holds and the policy, not the target table. Clients apply
   changes to their state; there is no separate poll and no merge.
   `GET /targets` returns the same snapshot once, for scripts.
   A new subscription may ask for a bounded history tail (`tail=N`, at most
   the ring size): the most recent flow records with sequence up to the
   snapshot's, filtered by its topics and kinds, delivered before any live
   change. Sequence numbers make each record arrive exactly once, so a
   monitor opened after an audit run can still draft rules from it.
2. **Generic event types.** `snapshot`, `flow`, `hold-open`, `hold-end`,
   `policy` (a rule of any kind added or removed), `target` (a row update or
   removal, `op: upsert|remove`; removal happens when the table cap evicts
   the least recently seen row, and it supersedes any pending rate-limited
   update for that row),
   `finding`, `status` (a status section update), `replay-end`, and `gap`. The kind is a
   field inside the data, never
   a new event type. Example:

   ```json
   {"instance":"7f3a…","seq":4812,"type":"flow","data":{
     "id":"f9312","parent":"f9307","kind":"http-request",
     "target":{"host":"api.github.com","port":"443"},
     "decision":"block","code":"request_not_allowlisted",
     "rule":"r17",
     "detail":{"method":"POST","path":"/repos/acme/x/issues"}
   }}
   ```
3. **Policy changes are events.** An allow, deny, or reject from any client
   (prompt, monitor, CLI, the desktop app) reaches every subscriber at once.
4. **Proxy instance ID.** A random ID per proxy start is part of every
   snapshot and event. A resume point is instance plus sequence; a different
   instance means a restart, detected for certain.
5. **A resume replays flow records only.** Every stream starts with a
   snapshot, so state is current after any reconnect, and the proxy keeps no
   log of `policy`, `target`, hold, or `status` changes to replay. A resume
   point (`after`) only selects flow records: those above it, up to the
   snapshot's sequence, come from the ring buffers before any live change,
   as with `tail`. If the resume point is from another instance or has
   fallen out of a ring buffer, the proxy sends a `gap` event before the
   replay instead of silently skipping entries. A subscriber dropped for
   falling behind reconnects with `after` like any other.
   The snapshot sequence is a state watermark. For a subscriber requesting
   flows, the snapshot also supplies the starting flow cursor for its replay
   window (including an initial bounded tail). Keep that cursor until a
   replayed flow has been applied, then advance it to that flow. End replay
   with a `replay-end` event at the snapshot watermark. Only then may the
   subscriber advance its flow cursor to the watermark and process live
   changes. Test disconnects just after the snapshot and midway through a
   tail or resume. Each reconnect delivers the remaining records or an
   explicit gap.
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
9. **Status section.** The snapshot carries a `status` section that
   `netproxy` maintains itself, and `status` events update it (rate-limited
   like target rows, topic `status`). It holds what a client shows about the
   proxy: instance ID and uptime; audit mode and other session settings;
   active connections and tunnels; open holds; flows per kind and decision
   since start; secrets loaded or missing. Status displays (TUI, desktop app)
   read it from the stream. They don't derive it from telemetry: telemetry
   lives in the proxy container, is tuned for external backends (metric
   names, temporality, sampling), can be switched off, and would need the
   OTel SDK in the binary. Values that appear in both are maintained
   separately; telemetry stays optional and external. An embedder may still
   wire an in-process metric reader for a debug view.

**Who turns holds on.** Today a request is held only while some subscriber
is on `/events` (`h.hub.Watching()`), and open holds end as "unwatched" when
the last one leaves. With topics, only subscribers of the `holds` topic count
as watching, and they count per holdable kind: a subscriber watches kind K
if it subscribes to `holds` and its kind filter includes K. A flow of kind K
is held only while a watcher for K exists, and when the last watcher for K
leaves, open K holds end as "unwatched". A client that reads only the
request log, or one that watches holds for `http-connect` only, must not make
other requests wait on a prompt nobody shows.

Kept from today: hold deadlines sent as time remaining (`hold_left_ms`), so
the proxy's and client's clocks don't need to agree; publishing that drops a
slow subscriber instead of blocking; keepalive comments.

### 9. TUI

- Logs tab: method, path (truncated, `tui.SanitizeText`), status, a
  tunnel/mitm marker, a secret-injected or secret-withheld marker, a "would
  block" marker in audit mode, highlighted alerts (a placeholder outside its
  binding, a credential the client sent itself), and failed client
  handshakes (the client doesn't trust the session CA).
- Hosts tab: tunnel/mitm marker; shadowed-rule warnings.
- On a refused request entry the monitor offers: allow exact path, allow a
  prefix, deny the path. These write object entries with literal hosts and
  paths to the project config. The sandbox chooses the path, and the pattern
  language has no escape, so a path whose decoded form contains `*`, `{`, or
  `}` gets no such action: saved as it is, `/api/*/x` or `/**` would be a
  wildcard the user never meant. The check uses the decoded path, so
  `/%2A%2A` is caught as well, and a path with an encoded separator (`%2F`,
  `%5C`) gets no action either. What is saved is the decoded path, which is
  the form patterns are written in (section 2). Today's monitor has the same rule for hosts (a host
  with a `*` is never allowable), and it carries over. The monitor never
  writes `secret`, `{set}`
  references, sets, or presets, so it cannot widen where a secret goes. A
  saved allow never changes the approved credential policy. A saved deny on
  an origin that has a credential grant narrows it (denies on that origin
  are part of the list). Either way the save is the user's own, so it keeps
  the project's approval (section 5) and the next startup doesn't ask.
- The approve screen and host-level holds are unchanged in behavior.
- At cutover, the feed (`cmd/feed.go`), the hosts tab, and the prompt are
  rebuilt on the new event stream (section 8): they apply the snapshot and
  changes instead of polling `/hosts` and merging, and drop their local
  guesses about allows, denies, and rejections. This removes most of
  `cmd/monitor_hosts.go`'s merge logic. The hosts tab shows the target table,
  all kinds included; decisions on targets go through `/decisions`, path
  actions through the rules endpoint.
- The prompt and monitor render the four known kinds specifically (e.g.
  "sandbox wants host port 3000") and any unknown kind generically: kind,
  target as a string, decision, and allow if the target is allowable.
- Draft rules view (section 7).

### 10. CLI and control API

- `allow-http` / `deny-http` commands gain `--method` and `--path` flags that
  produce object entries.
- New commands: `vibepit secret refresh`; `vibepit trust` (and
  `--revoke`), run in the project directory.
- New flags on `run`/`up`: `--audit` and the audit exclusion list
  (section 7).
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
secrets come back through the attached `run` or `secret refresh` (later the
daemon), and clients
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
- **The embedder supplies the proxy listener.** `ServeProxy(ln net.Listener)`
  serves ordinary HTTP proxy requests and `CONNECT` on a listener the caller
  has already opened. For example, vvmm can supply a TCP listener on its
  virtual gateway, or forward that port to a Unix socket served by the proxy.
  The embedder decides separately whether and where to serve the control API
  and guest DNS. This lets vvmm expose only the proxy port to the guest while
  the proxy resolves upstream names on the host.
- **The embedder supplies the resolver.** The dialer needs a name's CNAME
  chain and its addresses from one lookup (section 1), which `net.Resolver`
  can't give: it never reports the chain. `netproxy` defines a small
  interface for it (a name in; the chain and the addresses out) and ships
  one implementation that queries a given DNS server directly, as today's
  `lookupAliases` does. vibepit uses that one with `upstream-dns`. An
  embedder on the host can point it at the host's own resolver address, so
  VPN and split DNS keep working, or supply its own. The contract fails
  closed, as today: an implementation returns the whole chain or an error,
  never an empty chain for a lookup that failed. Addresses are IPv4 only for
  now. Names from `extra-hosts` don't go through the resolver; they come
  from a static table in the config (section 1).
- **In-process subscriptions:** an embedder that wants to persist runtime
  decisions subscribes to `policy` events and stores them however it likes.
- **`Close` leaves nothing behind.** vibepit's proxy process simply exits,
  but an embedder keeps running, so being disposable needs a contract.
  `Close` closes every listener given to the instance; ends open holds,
  refusing their requests; closes every connection it owns (tunnels,
  terminated connections, upgraded ones such as WebSockets, ordinary
  requests, and idle upstream connections); ends every event subscription;
  stops its timers; drops secret values; and returns once its goroutines
  have ended. The serve methods (`ServeProxy` and the others) then return.
  `Close` is immediate and may be called more than once; a graceful drain
  can be added later without changing it. `http.Server` forgets a connection
  once it is hijacked, so `netproxy` tracks tunnels and terminated
  connections itself. The same bookkeeping closes connections after a
  policy change (section 1) and feeds the status section.

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
  secrets loaded or missing. Some overlap with the stream's status section
  (section 8) on purpose: the status section serves clients, telemetry serves
  observability backends.
- **vibepit's side:** exporting is off by default. A later project can add
  global config for an exporter (OTLP/HTTP rather than OTLP/gRPC, which would
  noticeably grow the proxy binary). The remote branch `origin/add/otlp`
  goes the other way (the proxy receiving agents' OTLP telemetry); that is
  agent-specific, belongs to vibepit, and is not part of this design.

**In vibepit:** config files and merging, expanding sets and presets into
concrete rules (and the preset content), built-in service definitions,
secret sources, project trust, the `ProxyConfig` JSON container format,
mTLS
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
- New: the proxy config lives in the session directory, so it survives a
  reboot, in a directory of its own that is mounted into the proxy
  container as a directory. A single-file bind mount is tied to the file
  that existed when the container started; with the directory mounted, a
  file renamed into place is the one the proxy reads at its next start. The
  session's client credentials stay outside that directory. Unsaved
  decisions are lost on restart, as today.
- **Config files are read once, at session start.** The session's proxy
  config is the config resolved and approved at startup (global and project
  config, CLI inputs such as `-a` and `-p`, sets and presets expanded) plus
  the rules saved during the session, appended in order. Nothing reads the
  config files again mid-session. A re-read would pick up edits made since
  startup, such as a `git pull` that adds credential grants, and a proxy
  restart would then load them without the trust check (section 5). Manual
  edits to the config files take effect in the next session; the docs say
  so. `--no-save` decisions are never appended, so a restart drops them.
- **Saving a rule** happens in this order: apply it live through the control
  API, which returns the rule in canonical form; write the project config
  under its existing lock (`withProjectConfigLock`), keeping the approval
  (section 5); append the canonical rule to the session's proxy config
  under a session-directory lock, written to a temp file and renamed into
  place. Every append reads the file inside the lock, so two concurrent
  saves (CLI and monitor) can't drop each other's rules. The live response
  identifies its proxy instance. After the rename, confirm the running
  instance still enforces the rule. If the instance changed, idempotently
  ensure the canonical rule on the current instance before reporting success.
  A later restart loads the renamed file. Test a restart between the first
  acknowledgement and the rename.
- **What the UI reports:**
  - both writes succeeded and live enforcement was confirmed: "Saved";
  - both writes succeeded but live confirmation failed: report that the rule
    was saved and its current enforcement could not be confirmed;
  - the live change worked but the project write failed: "active for this
    session, not saved", as today; nothing is appended;
  - the project write worked but the append failed: "saved to the project
    config and active now, but a proxy restart in this session won't load
    it", with the error. The rule is in the project config, so the next
    session has it.
- `cmd/proxy.go` reads the file, translates it into `netproxy`'s config, sets
  up mTLS, adds the `host.vibepit` rewrite, and starts `netproxy` and the SSH
  forwarder.

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

**Behaviour carried over from `proxy/`.** goproxy's history is the checklist
for the data path (section 1); vibepit's own proxy is the checklist for
policy, holds, and DNS. Its fixes are easy to lose in a rewrite because many
of them are orderings and spellings, not features. Each item names the tests
that pin it today. Those tests are ported to `netproxy` on a small harness
(start a proxy from a config; get its proxy address, DNS address, and a
control client), with the control API calls translated to `/decisions` and
the rules endpoint. There is no runtime opt-in, so the ported suites
passing, together with the root `integration_test.go`, is the gate for the
cutover.

- **One spelling per target** (`TestCanonicalTarget`,
  `TestConnectChecksDialedPort`, `TestPlainHTTPChecksDialedPort`,
  `TestPlainHTTPForwardsToCanonicalTarget`, `TestPaddedAllowPortForwards`,
  `TestEnforcedDenyZeroPaddedPortCannotReachUpstream`,
  `TestEnforcedDenyTrailingDot`, `TestPlainHTTPUnknownSchemeBlocked`): hosts
  are lowercased and lose trailing dots, ports are decimal without padding
  and default to the scheme's, and IPv4-mapped IPv6 literals are unmapped.
  Every check, hold, flow record, and the dial use that one form, so no
  spelling gets around a deny. A target no dial could use (bad port, empty
  host, stray colon) is refused before any check or hold.
- **Deny wins, also in a race** (`TestEnforcedDenyFiltering`,
  `TestAllowAndDenySerialize`, `TestDeniedBetweenFilterAndHoldAdmission`,
  `TestDeniedConcurrentAllowRelease`,
  `TestHoldDeniedStaysBlockedDespiteRacingAllow`): rule changes are
  serialized, and a request the user denied never passes because an allow
  raced the deny.
- **An allow of a denied target is refused whole**
  (`TestAllowOfEnforcedDeniedTargetRejected`,
  `TestAllowRejectionNamesEveryDeniedEntry`,
  `TestAllowValidatesBeforeDenyCheckAndErrorsAreJSON`): nothing is applied,
  so the caller never saves an allow that can't take effect. The error is
  JSON and names every denied entry, quoted, because the names are the
  sandbox's choice and end up in a terminal.
- **A decision is recorded before its holds end, and a new hold checks again
  under its lock** (`TestHolds_AllowedBeforeRegistration`,
  `TestHolds_AllowRaceNeverStrands`, `TestHolds_DeniedBeforeRegistration`,
  `TestControlAPI_AllowHTTPEndsMatchingHolds`,
  `TestControlAPI_DenyEndsHold`): no request is left waiting on a target
  that was just decided.
- **A released hold is checked again** (`TestHTTPProxy_HeldThenAllowed`,
  `TestHTTPProxy_HeldThenCIDRBlocked`, `TestHTTPProxy_HeldCONNECTDenied`):
  the deny rules and the dial checks still apply after an allow.
- **Hold outcomes** (`TestHolds_ConcurrentRequestsShareOneHold`,
  `TestHolds_CancelledRequestLeavesOthers`,
  `TestHTTPProxy_AbandonedHoldNotLogged`,
  `TestHTTPProxy_RejectedResponseSaysSo`, `TestHolds_Limits`,
  `TestHTTPProxy_NoWatcherBlocksImmediately`, `TestHolds_NewHoldAfterEnd`):
  requests for one target share a hold; a client that left is not logged as
  a block; a target rejected for the session is never held again and the
  response says so; past the limits, or with nobody watching, requests are
  blocked at once.
- **Watchers leaving** (`TestHolds_LastWatcherLeaving`,
  `TestHolds_UnwatchedHookRechecksWatchers`,
  `TestHolds_DroppedWatcherKeepsHolds`,
  `TestEventHub_DropDoesNotFireOnEmpty`,
  `TestControlAPI_ClosingStreamEndsHolds`): holds end as unwatched when the
  last watcher leaves, but not when it was dropped for falling behind, since
  it reconnects.
- **Aliases and CIDR fail closed** (`TestHTTPRefusesAliasOfDeniedName`,
  `TestDNSRefusesAliasOfDeniedName`, `TestHTTPSkipsAliasLookupWhenUseless`,
  `TestAliasLookupAbandonedNotLogged`, `TestCheckRequestBlockCode`): a chain
  or an address that can't be looked up blocks, with its own code; a lookup
  that failed only because the client left is not logged as a DNS failure.
  Today the alias lookup is a second query, skipped when there are no deny
  rules and for IP literals. In `netproxy` the chain comes with the dial's
  own lookup (section 1), so there is nothing left to skip, and that test
  becomes "an IP literal needs no lookup".
- **DNS** (`TestDNSRefusesMultipleQuestions`, `TestDNSHostVibepit`,
  `TestDNSServerBlockedIPCode`): a message with more than one question is
  refused, since only the first would be checked; `host.vibepit` answers an
  A query with the proxy IP and other types with an empty answer.
- **Stream order under load** (`TestControlAPI_EventsNoGapUnderLoad`,
  `TestLogBuffer_OnAddInIDOrder`, `TestControlAPI_EventsReplayHoldOutcome`,
  `TestControlAPI_EventsOutlivesServerTimeouts`,
  `TestEventHoldDeadlineUsesReaderClock`): events leave in sequence order
  with none lost between history and live; a hold's outcome is on its record
  before the event goes out; the stream outlives the server's read and write
  timeouts.
- **Control API survives a handler panic** (`TestControlAPIPanicRecovery`):
  the caller gets a 500 and the proxy keeps running.
- **A host the sandbox named with a `*` is never allowable**
  (`TestLogEntryAllowable`): the sandbox chooses the host, and allowing
  `**.*:443` as it stands would open every host on that port. The same
  holds for paths with pattern syntax (section 9).
- **Saved denies survive a restart**
  (`TestPersistentDenyReloadIntegration`): a deny saved during a session
  still blocks after the proxy starts again from its config. This one stays
  in the root module: it goes through vibepit's config loading and saving,
  which `netproxy` doesn't have.

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

**Transition.** No runtime opt-in: `netproxy` has a new control API, so the
current CLI, monitor, and prompt couldn't talk to it. `netproxy` is built as a
module with its own tests, including the acceptance tests, while vibepit
keeps running on `proxy/`. Then vibepit switches over in one go: the glue in
`cmd/proxy.go`, config, CLI, monitor, and prompt move to `netproxy`'s API and
wire types, and `proxy/` is deleted.

Mixed versions are not supported, and that is accepted (vibepit is 0.x):
the new CLI, monitor, and prompt can't talk to a proxy started by an older
vibepit, or the other way round. Users end running sessions before they
update; the changelog and the update docs say so.

### 12. Docs

- `threat-model.md` (currently states the proxy does not inspect encrypted
  traffic) and `security-model.md`: TLS termination, the MITM CA, secret
  handling, global grants applying to every project, project trust, token
  scope as the primary control for writes, the server-name check on tunnels
  and what it leaves open (cleartext inside a tunnel, and whatever a server
  accepts that the check doesn't know yet), request rules binding a name and
  not a server (section 2), server-side branch protection as the only
  control for force pushes (section 2), placeholders that scripts
  reading the variable can't use as tokens,
  and that OAuth tokens from agent logins still live in the sandbox's home
  volume until the host daemon exists.
- Configuration reference for object entries, sets, presets with inputs,
  secrets, audit mode.
- **A troubleshooting file for the agent, inside the sandbox.** Modeled on
  `/root/.ccr/README.md` in Claude Code's cloud sandbox. An agent that hits
  a certificate error tends to turn verification off; one that hits a 403
  tends to retry or route around it. The file is written for the agent,
  ordered by failure class, each with its fix:
  - certificate errors: which variable or tool config file the tool reads
    (section 1), and pointing it at the bundle; never disable verification;
  - 403 with a vibepit body: a policy refusal; don't retry or route around
    it, report the host and path; allowing it is the user's action on the
    host (`allow-http`, the monitor, the prompt);
  - "secret not loaded": the user runs `vibepit secret refresh` on the host;
  - 400 for an absolute `https://` proxy request (use `CONNECT`), 421 (a
    connection reused for another host), and a TLS connection closed right
    after `CONNECT` (the tunnel's server-name check, section 1): report
    them, don't work around them;
  - a placeholder in `GH_TOKEN` and similar variables is not a token; there
    is no real one in the sandbox to find;
  - tools that ignore the proxy variables (Node's built-in `fetch` without
    `NODE_USE_ENV_PROXY=1`, aiohttp without `trust_env=True`), pinned
    clients, and outbound SSH.

  The text is embedded in the vibepit binary, so it always matches the
  running proxy, and is mounted read-only next to the sandbox's trust files
  (outside the home volume, like the CA). It is linked from vibepit's 4xx
  response bodies and the shell login banner, and the docs suggest pointing
  agent instruction files (`AGENTS.md`, `CLAUDE.md`) at it.
- Changelog and upgrade notes, collecting the breaks this design announces:
  end running sessions before updating (section 11); every project config
  asks for approval once (section 5); `host.vibepit` entries in HTTP rules
  are startup errors and wildcard allows no longer open host ports
  (section 8); sandbox tools verify with the proxy image's roots
  (section 1); a TLS tunnel is closed when its server name is missing or is
  not the `CONNECT` host, when its first record is not a ClientHello, and
  when it offers Encrypted ClientHello, including GREASE (section 1); an ordinary proxy request
  with an `https://` URL gets a 400, and an ordinary request goes upstream
  under its URL's host, whatever its `Host` header says (section 1); an
  unknown preset name is a startup error (section 4); a `deny-http` entry
  that matches an allowed host port is a startup error (section 8);
  upstream addresses are IPv4 only, and an IPv6 value in `extra-hosts` is a
  startup error (section 1); the project root is found without git, so
  `GIT_DIR`, `GIT_WORK_TREE`, and `core.worktree` no longer move it
  (section 5).

### 13. Behavior the tests must pin

**First acceptance test, written before the rest of `netproxy`:**
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
- Project trust: a project whose config is not approved doesn't start (`run`
  asks, `up` refuses), with or without credential grants; a hand edit of
  the project config asks again, and a change to comments or order doesn't;
  a save made through vibepit (setup, CLI, monitor, prompt) keeps the
  approval when the file was approved before it and doesn't when it wasn't;
  a change to the canonical credential policy without a project edit
  (built-in preset change, global edit to a grant the project contributes
  to) asks again; two checkouts with different project grants each get only
  their own plus the global ones; secret values or sources in a project
  config are startup errors; `audit` in project config is an error.
- Ordinary `http://` proxy requests get the same host admission as
  `CONNECT` (holds and host-level denies) and the same checks at their dial
  (alias denies and CIDR).
- An ordinary proxy request with an absolute `https://` URL gets a 400 and
  the upstream receives nothing.
- Alias and CIDR checks run on every upstream dial; the `host.vibepit`
  gateway route works while another name resolving to the same private
  address is blocked.
- A placeholder used as a query parameter name doesn't appear in the ring
  buffer entry or the `/events` output.
- After a proxy restart, before any values are pushed: a request that needs
  a secret gets the "not loaded" 403; a public request that carries the
  placeholder in the service's header goes upstream without it; a
  placeholder sent anywhere else is still blocked; no flow record contains
  a placeholder. In `run` mode the attached CLI pushes the values again
  after the restart, and injection works without `secret refresh`.
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
  client reaches every other subscriber; every stream starts with a
  snapshot, and a resume replays only the flow records after its point, each
  exactly once; a restarted proxy (new instance ID) and a resume point past
  the ring buffer both produce an explicit gap; a dropped slow subscriber
  that reconnects gets the flow records it missed or a gap, never silently
  fewer; topics and kinds filter what a subscriber receives, including the
  sections of the snapshot.
- Policy changes and open connections: a deny, or the first request rule
  for a host, added at runtime closes the open tunnels it covers, and the
  reconnect is refused or terminated; a deny on a name also closes a tunnel
  to an alias of it, from the chain kept at the dial; a tunnel to another
  host stays open; a terminated connection applies a new path deny to its
  next request without being closed; a new allow closes nothing. After a
  deny on a name, a request to an alias of it is refused even though an
  upstream connection to that server was still pooled.
- `when` ambiguity: a repeated query parameter or header that a condition
  references, a query string that doesn't parse cleanly, and a JSON body
  with a duplicate key each make an allow not match and a deny match.
- A client that rejects the session CA produces a failed-handshake flow
  record. In audit mode a host on the exclusion list is tunneled and its
  pinned client works; a host that rules terminate stays terminated; the
  exclusion list in a project config is an error.
- `Close`: with an open tunnel, a terminated connection with a request in
  flight, an upgraded WebSocket, an open hold, and a subscriber, `Close`
  returns, all of them have ended, no goroutine of the instance is left,
  and a new instance can serve on the same address at once.
- Resolver: `netproxy` runs with a resolver the caller supplies; a resolver
  error refuses the dial. A name from `extra-hosts` is reached through the
  static table without a DNS query, and its address still passes the CIDR
  check. `host.vibepit` works with the gateway given as an address.
- Generic protocol: every kind's flows, holds, and target rows go through
  the same event types, and decisions on allowable targets through
  `/decisions`; a client built without knowledge of `host-port` can still
  show and allow a held host port.
- `http-request` flows carry their `http-connect` as `parent`; a forwarded
  request's record appears when the response status arrives, before a long
  body ends; `/decisions` refuses an `http-request` target for allow, deny,
  and reject, and the rules endpoint allows the same request by method and
  path. A tunnel whose dial is refused has one `http-connect` record, the
  block; later events on a connection name it as `parent` and don't count
  as connections in the target row.
- Paths with pattern syntax: a refused request whose decoded path contains
  `*`, `{`, or `}` (sent literally or as `%2A`, `%7B`, `%7D`), or an encoded
  separator, offers no path action in the monitor and is left out of
  drafted rules; "allow exact path" on `/a%20b` saves `/a b` and that rule
  matches the same request.
- Active secrets: a configured secret without a grant sets no env variable
  and terminates nothing; with a grant on one binding host, the env has the
  placeholder and every allowed binding host is terminated, so a
  placeholder sent to another binding host is stripped, not tunneled; audit
  mode activates a secret that has no grant, except when one of its binding
  hosts is on the exclusion list; the client-credential alert fires for
  active secrets only.
- Placeholder scope: the service's header with the placeholder is stripped
  on a binding host and blocked on any other host, ordinary `http://`
  requests included; a different credential in that header raises the
  alert on a binding host and nothing elsewhere.
- Project root: a `core.worktree` in the project's git config, and a
  removed `.git` inside an outer repository, don't move the root; the trust
  prompt and first-time setup show the root path and name an approved
  project that the root contains or lies inside.
- Saves: two simultaneous saves (CLI and monitor) both end up in the
  session's proxy config; a failing append reports the partial save; a
  restart enforces a saved rule and drops an unsaved one; a project config
  edited by hand mid-session (for example a new credential grant) is not
  loaded by a save or by a proxy restart.
- Tunnel server name: a tunnel whose ClientHello names another host is
  closed with nothing sent upstream and a flow record naming both hosts; a
  matching name passes, also when the ClientHello arrives in the same write
  as `CONNECT` or split across several records; a ClientHello without a
  server name, with two names, or with the extension twice is refused; a
  tunnel whose first record is an alert, a ChangeCipherSpec, or application
  data is refused, including a warning alert followed by a ClientHello that
  names another host, and so is an SSLv2-format hello; after a
  HelloRetryRequest a second ClientHello that names another host is refused
  and one that keeps the name passes, also with a ChangeCipherSpec or early
  data before it; IP-literal and `host-port` targets are not checked;
  cleartext HTTP in a tunnel passes (Node's `fetch` to an `extra-hosts`
  name works); a tunnel whose first bytes are another protocol (SSH), and
  one where the server speaks first, work as today; a TLS start that can't
  be read within the limits is refused. These cases run against OpenSSL,
  Go, and Node servers, not only against the proxy's own parser, and the
  reading of the handshake is fuzzed.
- Every host in `github-read` and `github-write` is terminated; none is
  left as a whole-host entry, also when `default` is active next to them
  (`vcs-github` and `cdn-github` are replaced). The GraphQL presets add
  their one rule and replace nothing. A whole-host entry next to a
  restricted host under the same parent domain produces the startup
  warning. Checked by hand before the presets ship, since it needs the live
  network: Homebrew installs a bottle through the terminated `ghcr.io`.
- Alias check for request-level denies: a tunnel to an allowed name that
  aliases a name with a path deny is refused, with a reason that names it;
  the same name with a request rule of its own is terminated and reachable;
  a terminated connection is not refused for its chain; an ordinary request
  whose `Host` header names another host goes upstream under the URL's
  host.
- Dial refusals: a tunnel to a name that resolves into a blocked range gets
  a 403 on `CONNECT` and a blocked `http-connect` record; the same name
  inside a terminated connection, or as an ordinary request, gets a 403 on
  the request and a blocked `http-request` record with its parent; an
  unreachable upstream is a 502, not a block.
- A flow record's `rule` resolves to a rule in the snapshot that carries the
  origin the embedder set.
- Every item in "Behaviour carried over from `proxy/`" (section 11) has a
  ported test.
- An ordinary `http://` request produces an `http-connect` and an
  `http-request` (`mode: plain`) record; a path refusal on it is a refused
  `http-request` the monitor can act on.
- A subscription with `tail=N` receives a completed request from before it
  opened, and a request arriving while it opens exactly once.
- Evicting a target row emits `target` with `op: remove`, also while an
  update for that row is pending; the subscriber's table then equals a fresh
  snapshot.
- Holds are per kind: a watcher for `http-connect` only doesn't make
  `host-port` requests wait; when the last watcher for a kind leaves, that
  kind's holds end as unwatched.
- The status section in the snapshot matches the proxy's state, and
  `status` events keep it current with telemetry switched off.
- Telemetry: without providers, telemetry is a no-op; with test providers,
  spans and metrics appear with only bounded attributes; no span attribute or
  log field contains query values, placeholders, or secret values; upstream
  requests carry no trace headers; an incoming `traceparent` is ignored by
  default.
- Only subscribers of the `holds` topic enable holds; a subscriber to other
  topics alone gets no holds and no "unwatched" endings when it leaves.
- `netproxy` works without its HTTP control API through the Go API alone.
- A rule saved during a session is in the session's proxy config file, and a
  restarted proxy container enforces it, with the file renamed into place
  while the container ran; the file survives a reboot.
- `host-port`: traffic to `host.vibepit:<port>` produces `host-port` flows,
  is held when not allowed, and allowing it writes `allow-host-ports`; HTTP
  rules never decide it: a broad allow such as `*.*:*` loads and doesn't
  open a host port; an entry whose host pattern ends in `vibepit`, and a
  `deny-http` entry that matches an allowed host port (such as `*.*:3000`
  next to `allow-host-ports: [3000]`), fail at startup with the lines to
  write instead.
- Hosts without request rules or secrets are tunneled, not terminated.
- Older-format configs produce identical decisions, apart from the
  announced breaks (section 12).
- Deny wins over every allow, including preset and secret rules.
- Path normalization rejections; case rules for allows vs denies; a single
  trailing slash is ignored on pattern and path for allows and denies, and
  `//` is still rejected.
- Body cap and `when` error behavior for allows vs denies.
- Inspectors run before secret injection and see the agent's original
  headers; findings never contain the matched value.
- `request.graphql.queryOnly` is false for mutations, subscriptions, mixed
  documents, mutations after comments or fragments, batched arrays, and
  bodies with a duplicate `query` key.
- Secrets never appear in flow records, events, snapshots, API responses, or
  errors.
- Injection only within the service's host and path binding; placeholder
  stripping and misuse blocking. A stripped placeholder marks the flow
  record as secret withheld, also for a "would block" request in audit mode,
  which goes out without a credential. A credential the client sent itself
  is replaced when a grant matches and forwarded when none does, with an
  alert finding either way that doesn't contain it.
- Injection sets the header when the agent sent none, and replaces every
  value when it sent one or several; a git clone and push through a
  terminated connection work with no credential helper in the sandbox.
- A `*`, an IP address, or a trailing dot in a service's `hosts` is a
  config error, for built-in and inline services; a wildcard rule host with
  `secret` attaches the credential only on the binding's literal hosts.
- An inject entry bound to a listed public registry is a config error
  without `public-registry: true` and works with it.
- `placeholder` templates: invalid literals, a missing or repeated
  `{random:N}`, and `N` under 24 are config errors; a templated placeholder
  is stripped, detected outside its binding, and redacted like the default
  form.
- Two secrets with grants that overlap on origin, header, and path produce
  a startup warning; disjoint path patterns produce none; the overlapping
  request is still blocked at runtime.
- `request.git`: a push that creates or updates `refs/heads/*` matches the
  `github-write` push rule; a deletion, a tag, a `refs/notes/*` update, or a
  mixed push containing any of them gets a 403 with zero upstream requests.
  Parse errors (malformed pkt-line, missing flush within the cap, bad object
  ID, a ref name `check-ref-format` rejects, an unknown
  `Content-Encoding`) make an allow fail and a deny match. A
  gzip-encoded command list is parsed. A receive-pack `POST` with a
  misleading `Content-Type` is still parsed. A pack larger than the cap
  streams upstream without being buffered. Referencing `request.git.updates`
  on a non-push request is an evaluation error.
- `github-write` REST rules refuse ref deletion, ref creation outside
  `refs/heads/`, forced ref updates, and tag and release endpoints.
- Every sandbox CA variable of section 1 points at the bundle, and the
  troubleshooting file is mounted read-only, outside the home volume, in
  every way into the sandbox listed under sandbox trust below.
- HTTP/2 and WebSocket requests through a terminated connection.
- Sandbox trust: the PEM bundle and the Java truststore hold the proxy's
  roots plus the session CA; the truststore loads in the oldest supported
  JDK and a current one, with and without an explicit store type, and a
  leaf signed by the session CA is accepted; the env and the mounts are
  there in a first shell, an exec'd shell, a daemon-mode shell, and
  `vibepit exec`; with the variables unset, the system's `curl` still
  trusts a terminated host, because the bundle is mounted over the system
  one.
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

## Future: approving grants on first use

Not built in this project; `vibepit trust` covers project grants for now.
Recorded because the protocol already fits it.

- **Idea.** Instead of approving a project's whole credential policy at
  startup, the proxy holds the first request that needs an unapproved
  project grant, and the prompt shows it in context: "~/code/acme wants to
  send secret `github` to api.github.com for `GET /repos/acme/api/**` (rule
  from github-read)", with allow for this project, allow for this session,
  or reject. Only grants that are actually used get asked about, and a
  config change asks again only when the changed grant is used.
- **Protocol fit.** A holdable kind such as `credential-grant` with target
  `{project, secret, grant}`. Holds, `/decisions`, the event stream, and the
  per-kind watcher rule carry it without anything new. The number of grants
  is small and fixed by config, so unlike general request-level holds there
  is no grouping problem.
- **Shared with `vibepit trust`.** The same per-grant identity (canonical
  grant including the denies on its origin) and the same store on the host;
  `vibepit trust` becomes batch approval of open grants, still needed for
  `up` with nobody attached. Storing approvals stays vibepit's job;
  `netproxy` only learns that a grant is approved.
- **To decide then.** Whether a request after a rejected grant gets a 403 or
  goes out without the credential (placeholder stripped, so public reads keep
  working).

## Future: replacing the config at runtime

Not built in this project: nothing in vibepit would call it. Recorded with
its open questions, because an embedder that reloads its config will want
it.

- **Idea.** `ApplyConfig(cfg)` replaces the whole policy of a running
  instance atomically and publishes the change on the event stream, so an
  embedder can reload from any source without dropping connections. vibepit
  could later watch the session's proxy config and call it.
- **Until then** an embedder reloads by calling `Close` and starting a new
  instance with the new config. That drops connections, holds, history, and
  loaded secret values, which the no-durable-state principle already allows
  for.
- **Open questions.**
  - Runtime decisions that are not in the new config (unsaved allows and
    denies, session rejections): kept on top of it, or dropped?
  - Loaded secret values: kept when a secret's name and service binding are
    unchanged, dropped when the binding changed?
  - Open holds: evaluated again, released when the new policy allows the
    target and ended when it denies it?
  - Open connections: presumably the rule from section 1 (close those whose
    outcome became stricter).
  - Scope: policy only, or also host rewrites, CIDR ranges, the resolver,
    the hold timeout, audit mode, and inspectors?
  - Events: one `policy` event per rule that differs, or one event that
    carries the new policy?
  - A request that is being authorized during the swap must see the old
    policy or the new one, never a mix.
  - For vibepit's file watching: the config directory is already mounted
    as a directory (section 11), so a watcher in the proxy would see a file
    renamed into place.

## Left to the builder

- Verify the preset expressions and evaluation-error semantics with the
  condition engine before its syntax becomes public config (section 2).
- Exact names of new config keys for user-defined presets, the audit
  setting, and its exclusion list, the new control API endpoint, and new
  block codes.
- The exact GitHub preset rule lists (verify paths against GitHub's git smart
  HTTP, REST, and LFS docs), including the REST ref, tag, and release
  endpoints `github-write` refuses.
- How a refused push reads in git's output: a `403` with a `text/plain`
  body, or a `200` carrying a `report-status` `ng <ref> <reason>` line per
  refused ref, which git prints next to the ref. The decision stays
  per request either way.
- The exact public-registry list, and which built-in services need a
  `placeholder` template (check each client for local token checks).
- Confirm each sandbox CA variable against current tool versions and drop
  any that a tool no longer reads.
- The troubleshooting file's mount path (next to the trust files) and its
  final wording.
- Leaf certificate cache size and lifetime; CA lifetime matching the existing
  session credentials.
- Mount paths of the sandbox's trust files, and which JDK versions the
  truststore test covers.
- Where the trust record is stored (per `config/paths.go`) and the exact
  canonical form that is hashed.
- The draft-rules output mechanism (view plus copy, or write to a file).
- Monitor key bindings for the new actions, consistent with `a`/`A`/`N`.
