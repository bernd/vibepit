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
- goproxy v1.9.4 is the proxy library (supports MITM, HTTP/2, WebSockets).
- The control API is mTLS and reachable only from the host.
- The proxy container restarts on its own (`unless-stopped`).
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
- *goproxy `MitmConnect`, tightly wrapped (selected):* smallest change; a
  test suite pins the behavior we rely on so a later switch is safe.
- *Own `crypto/tls` + `httputil.ReverseProxy` for CONNECT:* full control,
  but a second proxy stack on the most security-sensitive path.
- *Replace goproxy entirely:* rewrites working code without need.

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
  `host.vibepit`. Other ports may carry non-HTTP protocols that goproxy's MITM
  path would break, so in audit mode they stay tunneled and are logged at the
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
server certs) and are cached per host. goproxy's built-in `GoproxyCa` must
never be used; its private key is public.

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

**Inside a terminated connection.**
- The request's authority must equal the `CONNECT` target (default port
  applied), for both forms: the inner `Host` header or HTTP/2 `:authority`
  for origin-form requests, and the URL's host for absolute-form requests.
  goproxy prefers the inner `Host` when building the upstream URL
  (`https.go`, "We prioritize req.Host") and keeps an absolute-form URL's
  host, so a mismatch is rejected, never followed. The checked target is
  carried in `ctx.UserData`.
- The client must speak TLS. goproxy serves a non-TLS connection after
  `CONNECT` as cleartext `http` (including h2c); a terminated connection
  rejects that instead.
- Blocked requests get a real `403` with an explanatory body (today a blocked
  HTTPS host only shows a failed connection).
- HTTP/2 is enabled (`AllowHTTP2`). WebSocket upgrade requests are evaluated
  like any request; frames are not inspected.
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
    - hosts: [api.github.com]
      paths: ["/**"]
      header: Authorization
      auth: bearer
    - hosts: [github.com]
      paths: ["/*/*.git/**", "/*/*/info/refs", "/*/*/git-upload-pack", "/*/*/git-receive-pack"]
      header: Authorization
      auth: {basic: x-access-token}
```

`auth` styles: `bearer`, `token`, `{basic: <user>}`, `raw`.

Each inject entry binds to an **HTTPS origin**: scheme `https`, the listed
hosts, and an explicit port (`port`, default 443). A rule with `secret` whose
port differs from every binding is a config error.

**Secrets** are configured in global config only:

```yaml
secrets:
  github:                             # uses the built-in "github" service
    source: {command: "gh auth token"}
  my-api:                             # inline service
    source: {env: MY_API_KEY}
    env: [MY_API_KEY]
    inject:
      - hosts: [api.example.com]
        header: X-Api-Key
        auth: raw
```

Sources: `env` and `command`.

**Delivery.**
- `run` and `up` resolve sources on the host at startup and push values to
  the proxy through a new mTLS control API endpoint. Values are held in proxy
  memory only, never in `ProxyConfig`, container env, `GET /config`, or any
  response.
- `vibepit secret refresh` resolves and pushes again (for expired tokens).
- After a proxy restart no values are loaded. Requests that need a secret get
  a 403: "secret `github` not loaded, run `vibepit secret refresh`".

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
- **Wire types:** a finding is carried on the log entry and the `/events`
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

- `LogEntry` gains `Method`, `Path`, `Query`, `Status`, `Mode`
  (tunnel/mitm), `Secret` (name only), and an audit marker.
- Query strings: parameter names only, in every mode including audit. Signed
  download URLs carry credentials vibepit doesn't know about. Draft rules
  need only host, method, and path.
- Never logged: headers, bodies, query values, secret values, placeholders. A
  placeholder block logs "placeholder for secret `github` sent outside its
  binding".
- Log entries are sanitized when they are built, before they enter a ring
  buffer or `/events`: known placeholders and secret values are replaced with
  `<secret:NAME>` in every retained field derived from the request (host,
  path, query parameter names, reason text). Redacting only the inspector's
  finding is not enough, since the request entry itself carries those
  fields.
- New block codes for request-rule refusals, host mismatch, placeholder
  misuse, and missing secret values. `AllowFixes` is false for all except a
  request-level "not allowlisted".
- Request entries go into their own ring buffer (10k), so floods don't push
  out host-level blocks. `/events` carries both.
- `HostStats` gets separate `Connections` and `Requests` counters.

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
- The approve screen and host-level holds are unchanged.
- Draft rules view (section 7).

### 10. CLI and control API

- `allow-http` / `deny-http` commands gain `--method` and `--path` flags that
  produce object entries.
- New commands: `vibepit secret refresh`, `vibepit trust`.
- Control API: allow and deny endpoints accept object entries; a new endpoint
  receives secret values; `GET /config` never includes them.

### 11. Packaging: the `netproxy` module

The new proxy is a nested Go module, `netproxy/` (module
`github.com/bernd/vibepit/netproxy`), that imports no vibepit packages, so it
can be reused in other projects. The existing `proxy/` package stays
untouched until the new one is done.

**In `netproxy`:** policy (allowlists, deny rules, request rules, path
matching, conditions), TLS termination (CA and leaf certificates, `Host`
pinning, tunnel-or-terminate decision), secret injection from service
definitions and values the caller supplies, the request inspector interface
and pipeline stage, holds, event hub, log buffers, DNS server, CIDR blocking, and the control API as an `http.Handler` with its
wire types. It takes a typed Go config containing only concrete rules.

**In vibepit:** config files and merging, expanding sets and presets into
concrete rules (and the preset content), built-in service definitions,
secret sources, project trust, the `ProxyConfig` JSON container format, mTLS
setup from the `VIBEPIT_PROXY_*` env vars, CA common names, user-facing
message text, `host.vibepit`, the SSH forwarder, sandbox trust setup, and the
TUI.

**Things `netproxy` makes configurable instead of hard-coding:** block
message text, host rewrites (`host.vibepit` becomes a general name → gateway
rule with allowed ports), the control API's `tls.Config`, and neutral header
names (no `X-Vibepit-*`).

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
- Log entries in the ring buffers and on `/events` contain no query values,
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
- Hosts without request rules or secrets are tunneled, not terminated.
- Older-format configs produce identical decisions.
- Deny wins over every allow, including preset and secret rules.
- Path normalization rejections; case rules for allows vs denies.
- Body cap and `when` error behavior for allows vs denies.
- Inspectors run before secret injection and see the agent's original
  headers; findings never contain the matched value.
- `request.graphql.queryOnly` is false for mutations, subscriptions, mixed
  documents, mutations after comments or fragments, and batched arrays.
- Secrets never appear in logs, events, `GET /config`, or errors.
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
