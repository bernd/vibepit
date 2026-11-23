# First netproxy acceptance milestone

Implement section 13 of the [inspection design](../specs/2026-11-17-mitm-inspection-design.md#13-behavior-the-tests-must-pin) in the nested Go module. Keep the CLI on the existing proxy until the cutover suite passes. The first handler is package-private and serves explicitly configured inspection origins. The embedder supplies each origin's checked address and leaf certificate. Blind tunnels, session CA issuance and runtime policy belong to later milestones.

1. Drive ordinary HTTP, HTTP/1.1 over CONNECT and HTTP/2 over CONNECT through real loopback listeners. For each, check the response status, negotiated protocol and upstream call count for a whole-host allow with a path deny, a path-only allow and a condition on `request.path`.
2. Implement shared request authorization in `netproxy/policy.go`. Compile path conditions at construction, preserve the outbound escaped path and test both path interpretations. Verify malformed policies fail to load.
3. Implement the proxy entry point in `netproxy/http.go`. Route by canonical authority, require TLS inside CONNECT, bind requests to the admitted origin and verify the upstream certificate. Test authority mismatch, refused request forms and upstream verification.
4. Include the nested module in the Make test targets. Run the module race tests and the root unit and integration suites. Reconcile the six readiness findings in the design before work begins on their respective subsystems.
