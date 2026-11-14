---
description: Threats Vibepit defends against, such as network exfiltration, lateral movement, and privilege escalation, and its known limitations.
---

# Threat Model

For the specific controls that implement these defenses, see the [Security Model](security-model.md). For how the components fit together at runtime, see the [Architecture](architecture.md).

## Primary attacker profile

Vibepit assumes the main threat is a **compromised or misbehaving AI coding agent** running inside the sandbox container. The agent has shell access and can run arbitrary code in the container. It may try to exfiltrate data, reach external services, pivot to the host, or persist malicious changes.

Assume an active process with full user-level access to a Linux environment that will try every tool available to it.

### How an agent gets compromised

An agent does not need to be malicious by design. A well-behaved agent can turn hostile in several ways:

- **Prompt injection.** Malicious instructions hide in issue descriptions, pull request comments, code review feedback, README files, or web pages that the agent reads. The agent follows them as if they came from you and may exfiltrate code, install backdoors, or change files in ways you did not intend.
- **Compromised dependencies.** A supply chain attack on a package the agent installs gives the attacker code execution inside the container, in a malicious process running next to the agent.
- **Tool-use exploits.** MCP servers, shell tools, or other integrations that the agent calls can return crafted responses that take over the agent's behavior.
- **Malicious agent skills or plugins.** Third-party skills, extensions, or plugins loaded into the agent can contain arbitrary code or prompt overrides that change the agent's behavior without your knowledge.

!!! note
    **Vibepit does not prevent these attacks. It limits what a compromised
    agent can do.** Without network isolation, a prompt-injected agent with
    shell access can send your source code, credentials, and environment
    variables anywhere in seconds. With Vibepit, that agent can only reach the
    domains you allowed.

## Trust boundaries

Vibepit defines four trust boundaries, each with a different level of trust.

**Host system. Fully trusted.** The host runs the `vibepit` CLI and the container runtime, and owns the kernel. If the host is compromised, Vibepit's controls provide no protection. Vibepit does not try to protect against a malicious host.

**Proxy container. Trusted, with a minimal attack surface.** The proxy runs on a distroless base image with no shell or package manager. It enforces network policy and exposes a control API protected by mTLS. Vibepit trusts the proxy to apply allowlist rules correctly and block everything else.

**Sandbox container. Untrusted.** The agent runs here. Vibepit treats everything inside the sandbox container as potentially hostile. The agent has user-level shell access and can run any code the `code` user can run.

**Network boundary. Enforced by the proxy.** The only path from the sandbox container to the internet goes through the proxy, which filters DNS, HTTP, and HTTPS traffic against the allowlist. The sandbox container has no direct route to the host network or the internet.

## In scope: what Vibepit defends against

### Network exfiltration

The sandbox container starts with no network access. Every outbound HTTP/HTTPS connection goes through the proxy's allowlist, which matches on domain and port. The proxy rejects connections to destinations not on the allowlist, so the agent cannot upload source code, credentials, or other data to arbitrary servers.

### Data exfiltration via DNS

DNS queries from the sandbox container go to the proxy's DNS server. Only domains on the DNS allowlist get valid responses, and all other queries return `NXDOMAIN`. This blocks DNS tunneling and other techniques that encode data in queries to non-allowlisted domains.

### Lateral movement to host or other containers

The sandbox container runs on an internal Docker network with no default gateway to the host. The proxy enforces a CIDR blocklist that rejects connections to all private IP ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), and link-local addresses. If the agent gets a domain to resolve to a private IP, the proxy still blocks the connection. The agent cannot reach the Docker host, the container runtime API, or other services on your local network.

### Privilege escalation inside the container

The sandbox container drops all Linux capabilities, enables `no-new-privileges`, and runs as the unprivileged `code` user. The agent cannot use `CAP_NET_RAW` to craft raw packets, `CAP_SYS_ADMIN` to manipulate namespaces, or setuid binaries to gain root. These controls don't make escalation impossible, but they remove the most common paths.

### Persistent filesystem compromise

The container's root filesystem is read-only. The agent cannot change system binaries, libraries, or OS-level configuration. The only writable storage is `/tmp` (tmpfs, cleared on container stop) and the persistent home volume, so the agent cannot install rootkits or backdoors into the base image.

## Out of scope: what Vibepit does not defend against

### Container escape via kernel vulnerabilities

The sandbox container shares the host kernel. A kernel vulnerability that allows escape from the container namespace gives the agent full host access. Vibepit's hardening (dropped capabilities, no-new-privileges, non-root user) leaves fewer kernel interfaces to exploit, but cannot eliminate this class of vulnerability.

### Host compromise

If the container runtime (Docker or Podman) is compromised, or the host is misconfigured (for example, by mounting the Docker socket into the sandbox container), an attacker can bypass Vibepit's controls entirely. Vibepit assumes the host and container runtime are trustworthy.

### Supply chain attacks in allowlisted dependencies

When you allowlist a domain like `registry.npmjs.org`, Vibepit permits all traffic to that domain. If a package on that registry is compromised, the malicious code runs inside the sandbox container with the same access as the agent. Vibepit filters by network destination, not by payload content.

### Side-channel attacks

Vibepit does not defend against timing attacks, cache-based side channels, or other techniques that leak information without using the network. Network-level isolation does not cover them.

### Social engineering via agent output

Vibepit filters network traffic, not terminal output. A compromised agent can display misleading instructions or fake error messages, or talk you into acting for it, such as expanding the allowlist or running commands on the host. Reviewing what the agent tells you is your job.

### Blocked connection prompts

With `--prompt`, or `prompt: true` in the global config, a blocked request
from the sandbox opens an allow/deny prompt over your session. See the
[CLI reference](../reference/cli.md#blocked-connection-prompt). This gives
the agent a way to put a question in front of you. It can make requests on
purpose to trigger prompts, and it chooses the domain names they show. A
domain can be made to look legitimate, for example
`api-anthropic.example.net`.

The decision stays with you. The prompt runs on the host, and it talks to
the proxy's control API with the host-only mTLS credentials, which never
enter the sandbox. While the prompt shows, Vibepit holds back the agent's
output, and your keys go to the prompt only after the terminal confirms the
switch. The agent can't draw over the prompt or read your answer. The domain
and reason shown come from the sandbox's own request, so Vibepit strips
control characters and escape sequences before displaying them. The agent can
print text that looks like a prompt, but the keys you type into it go to the
agent, and the proxy allows nothing without the real prompt.

Each target prompts at most once per session, and targets you deny stop
prompting on every client. An agent can still produce many prompts by
requesting many different domains. Read the domain in each prompt before
pressing `a`, and dismiss or deny anything you do not recognize.

## Residual risks

Vibepit has known limitations even within its scope.

**Container escapes exist.** Researchers find new container escape vulnerabilities regularly. Vibepit's hardening leaves fewer interfaces to exploit, but an attacker with a zero-day kernel or runtime exploit can escape.

Some runtimes run containers inside a Linux VM: Docker Desktop (all platforms) and Podman Machine (macOS/Windows). With these, a container escape lands in the VM guest rather than on your host. Docker Engine and Podman on Linux have no VM boundary, and containers share the host kernel directly. The runtime VMs are not built as security boundaries, so do not rely on them. For stronger isolation, run Vibepit inside a dedicated VM.

**DNS rebinding against allowlisted domains.** If you allowlist a domain and an attacker controls its DNS records, they can make it resolve to another IP. The CIDR blocklist catches private ranges, but if the domain points to a public IP you did not intend to allow, the connection succeeds. Keep your allowlist as narrow as possible.

**Covert channels via allowed connections.** If the agent can reach an allowlisted domain, it can use that connection to exfiltrate data.

The most obvious example is the agent's own provider API. The agent needs `api.anthropic.com` or `api.openai.com` on the allowlist to work, and every API request can carry data in prompts, tool calls, or context. Web search and file upload features send more data through the provider's servers. An agent with access to `github.com` could push data to a repository it controls.

Vibepit cannot tell legitimate use of an allowlisted domain from abuse. Allow only the domains the agent needs. A MITM proxy that terminates TLS could inspect payloads and catch some exfiltration patterns, but Vibepit uses CONNECT tunneling and does not inspect encrypted traffic. Even with content inspection, an attacker can encode data in requests that look legitimate, so inspection would only make exfiltration harder, not impossible.

## Mitigations and their limits

The [Security Model](security-model.md) describes each control in detail: default-deny networking, container hardening, CIDR blocking, DNS filtering, HTTP/HTTPS filtering, and the mTLS control API. The [Architecture](architecture.md) explains how these components connect at runtime.

Vibepit is one layer of defense. To get the most from it:

- **Review agent output.** Vibepit cannot tell you when the agent is lying. Read what it produces before acting on it.
- **Minimize your allowlist.** Every domain you allow is a possible exfiltration path. Allow only what the agent needs for the current task.
- **Keep your container runtime updated.** Docker and Podman regularly patch container escape vulnerabilities. An outdated runtime weakens every isolation control.
- **Consider VM-level isolation for sensitive workloads.** Running Vibepit inside a VM adds a second boundary that does not depend on the container runtime or the shared kernel.

No tool makes running untrusted code safe. Vibepit narrows what a compromised agent can reach, and this page lists where that protection ends.
