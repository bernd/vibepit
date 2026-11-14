---
description: Run LLM providers on your local LAN when you're using private IP addresses and a local DNS server.
---

# Run with a Local LLM Provider

This guide lets the sandbox reach an LLM provider such as Ollama, LM Studio, or
llama.cpp on your LAN, while the proxy keeps blocking the rest of your local
network.

## Allow access to the LLM provider on your local network

The examples below assume your LLM provider (for example Ollama) runs on a
machine named `llm-server` on your LAN.

By default, Vibepit blocks RFC 1918 private IP ranges and other reserved ranges
so the sandbox cannot reach services on your local network. Open only the range
where your LLM provider runs and keep everything else blocked.

Add your subnet, or single IPs, to the `allow-cidr` list in your global config
(`~/.config/vibepit/config.yaml`):

```yaml
# Exempt these ranges from the default CIDR blocklist.
allow-cidr:
  - 192.168.1.0/24 # for the full subnet
  - 192.168.1.2/32 # for a single IP

# Let the proxy forward requests to the local LLM provider API.
allow-http:
  - llm-server:8000
```

The sandbox can now reach the listed addresses, and the default `block-cidr`
rules still block all other private ranges.

!!! note

    `allow-cidr` takes precedence over `block-cidr`. If both lists contain
    overlapping ranges, the allow entry wins.

## Configure the upstream DNS resolver

If your LAN uses a local DNS server (for example Pi-hole, AdGuard Home, or a
corporate DNS), add `upstream-dns` to your global config to point Vibepit's DNS
proxy at it:

```yaml
upstream-dns: 192.168.1.1:53
```

The proxy sends every DNS query that passes the allowlist to your LAN resolver,
so internal hostnames resolve along with public domains.

## Use extra-hosts instead of changing the resolver

If you don't want to change the upstream resolver, add the hosts you need with
`extra-hosts`:

```yaml
extra-hosts:
  - llm-server:192.168.1.2
```

## Example: configure OpenCode

Add the following `provider` section to `~/.config/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "llm-server": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://llm-server:8000/v1"
      }
    }
  }
}
```
