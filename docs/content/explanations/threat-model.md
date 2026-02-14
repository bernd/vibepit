# Threat Model

This page describes the threats Vibepit is designed to address, where its security boundaries are, and what falls outside its scope. For the specific controls that implement these defenses, see the [Security Model](security-model.md). For how the components fit together at runtime, see the [Architecture](architecture.md).

## Primary attacker profile

Vibepit assumes the primary threat is a **compromised or misbehaving AI coding agent** running inside the sandbox container. The agent has shell access and can execute arbitrary code within the container. It may attempt to exfiltrate data, reach external services, pivot to the host, or persist malicious changes.

You are not defending against a passive observer. You are defending against an active process with full user-level access to a Linux environment that will try every tool available to it.

### How an agent gets compromised

An agent does not need to be intentionally malicious. Several realistic attack vectors can turn a well-behaved agent into a hostile one:

- **Prompt injection.** Malicious instructions hidden in issue descriptions, pull request comments, code review feedback, README files, or web pages that the agent reads. The agent follows these instructions as if they came from you, potentially exfiltrating code, installing backdoors, or modifying files in ways you did not intend.
- **Compromised dependencies.** A supply chain attack on a package the agent installs gives the attacker code execution inside the container. The agent then operates in an environment where a malicious process is running alongside it.
- **Tool-use exploits.** MCP servers, shell tools, or other integrations that the agent calls can return crafted responses that hijack the agent's behavior.
- **Malicious agent skills or plugins.** Third-party skills, extensions, or plugins loaded into the agent can contain arbitrary code or prompt overrides that alter the agent's behavior without your knowledge.

!!! note
    Vibepit does not prevent these attacks from happening — it limits the damage
    by constraining what a compromised agent can do. Without network isolation,
    a prompt-injected agent with shell access can exfiltrate your source code,
    credentials, and environment variables in seconds. With Vibepit, that same
    agent can only reach the domains you have explicitly allowed.

## Trust boundaries

Vibepit defines four trust boundaries, each with a different level of trust:

**Host system — fully trusted.** The host runs the `vibepit` CLI, the container runtime, and owns the kernel. If the host is compromised, all bets are off. Vibepit does not attempt to protect against a malicious host.

**Proxy container — trusted, minimal attack surface.** The proxy runs on a distroless base image with no shell or package manager. It enforces network policy and exposes a control API secured by mTLS. The proxy is trusted to faithfully apply allowlist rules and block unauthorized traffic.

**Sandbox container — untrusted.** This is where the agent runs. Vibepit treats everything inside the sandbox container as potentially hostile. The agent has user-level shell access and can run any code that the `code` user can execute.

**Network boundary — enforced by proxy.** The only path from the sandbox container to the internet passes through the proxy. DNS, HTTP, and HTTPS traffic are all subject to allowlist filtering. There is no direct route from the sandbox container to the host network or the internet.

## In scope: what Vibepit defends against

### Network exfiltration

The sandbox container starts with zero network access. All outbound HTTP/HTTPS connections must pass through the proxy's allowlist, which matches on domain and port. Connections to destinations not on the allowlist are rejected. This prevents the agent from uploading source code, credentials, or other data to arbitrary servers.

### Data exfiltration via DNS

DNS queries from the sandbox container go to the proxy's DNS server. Only domains on the DNS allowlist receive valid responses; all other queries return `NXDOMAIN`. This blocks DNS tunneling and other techniques that encode data in DNS queries to non-allowlisted domains.

### Lateral movement to host or other containers

The sandbox container runs on an internal Docker network with no default gateway to the host. The proxy enforces a CIDR blocklist that rejects connections to all private IP ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), and link-local addresses. Even if the agent tricks a domain into resolving to a private IP, the connection is blocked. This prevents the agent from reaching the Docker host, the container runtime API, or other services on your local network.

### Privilege escalation inside the container

The sandbox container drops all Linux capabilities, enables `no-new-privileges`, and runs as the unprivileged `code` user. This means the agent cannot use `CAP_NET_RAW` to craft raw packets, `CAP_SYS_ADMIN` to manipulate namespaces, or setuid binaries to gain root. These controls do not make escalation impossible, but they remove the most common paths.

### Persistent filesystem compromise

The container's root filesystem is mounted read-only. The agent cannot modify system binaries, libraries, or OS-level configuration. Writable storage is limited to `/tmp` (tmpfs, cleared on container stop) and the persistent home volume. This prevents the agent from installing rootkits or backdoors into the base image.

## Out of scope: what Vibepit does not defend against

### Container escape via kernel vulnerabilities

The sandbox container shares the host kernel. A kernel vulnerability that allows escape from the container namespace gives the agent full host access. Vibepit's hardening (dropped capabilities, no-new-privileges, non-root user) reduces the attack surface for kernel exploits, but cannot eliminate this class of vulnerability.

### Host compromise

If the container runtime (Docker or Podman) is itself compromised, or if the host is misconfigured (for example, mounting the Docker socket into the sandbox container), Vibepit's controls can be bypassed entirely. Vibepit assumes the host and container runtime are trustworthy.

### Supply chain attacks in allowlisted dependencies

When you allowlist a domain like `registry.npmjs.org`, Vibepit permits all traffic to that domain. If a package on that registry is compromised, the malicious code runs inside the sandbox container with all the access that entails. Vibepit filters by network destination, not by payload content.

### Side-channel attacks

Vibepit does not defend against timing attacks, cache-based side channels, or other techniques that leak information without using the network. These attacks are outside the scope of network-level isolation.

### Social engineering via agent output

Vibepit filters network traffic, not terminal output. A compromised agent can display misleading instructions, fake error messages, or manipulate you into taking actions on its behalf (such as expanding the allowlist or running commands on the host). You remain responsible for reviewing what the agent tells you.

## Residual risks

Even within its scope, Vibepit has known limitations that you should understand.

**Container escapes exist.** New container escape vulnerabilities are discovered periodically. Vibepit's hardening reduces the exploitable surface, but a sufficiently capable attacker with a zero-day kernel or runtime exploit can escape. Some runtimes — Docker Desktop on all platforms, Podman Machine on macOS/Windows — run containers inside a Linux VM, so a container escape lands in the VM guest rather than on your host. Docker Engine and Podman on Linux run containers directly on the host kernel with no VM boundary. These runtime VMs are not designed as security boundaries, so they should not be relied upon as a guarantee. For higher-assurance isolation, run Vibepit inside a dedicated VM.

**DNS rebinding against allowlisted domains.** If you allowlist a domain and an attacker controls its DNS records, they can make it resolve to a private IP. The CIDR blocklist catches private ranges, but if the rebinding targets a public IP that you did not intend to allow, the connection succeeds. Keep your allowlist as narrow as possible.

**Covert channels via allowed connections.** If the agent can reach an allowlisted domain, it can use that connection to exfiltrate data. For example, an agent with access to `github.com` could push data to a repository it controls. Vibepit cannot distinguish legitimate use of an allowlisted domain from abuse. Limit your allowlist to domains the agent genuinely needs. A MITM (man-in-the-middle) proxy that terminates TLS could inspect payloads and detect some exfiltration patterns, but Vibepit currently uses CONNECT tunneling and does not inspect encrypted traffic. Even with content inspection, a determined attacker can encode data in legitimate-looking requests, making this an arms race rather than a complete solution.

## Mitigations and their limits

The [Security Model](security-model.md) describes each control in detail: default-deny networking, container hardening, CIDR blocking, DNS filtering, HTTP/HTTPS filtering, and the mTLS control API. The [Architecture](architecture.md) explains how these components connect at runtime.

Vibepit is one layer of defense. To get the most from it:

- **Review agent output.** Vibepit cannot tell you when the agent is lying. Read what it produces before acting on it.
- **Minimize your allowlist.** Every domain you allow is a potential exfiltration path. Allow only what the agent needs for the task at hand.
- **Keep your container runtime updated.** Docker and Podman regularly patch container escape vulnerabilities. An outdated runtime weakens every isolation control.
- **Consider VM-level isolation for sensitive workloads.** Running Vibepit inside a VM adds a second isolation boundary that is independent of the container runtime and kernel.

No tool can make running untrusted code safe. Vibepit reduces the risk to a level where the productivity benefits of AI coding agents can outweigh the security cost — provided you understand the boundaries described on this page.
