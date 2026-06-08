---
description: Run LLM providers on your local LAN when you're using private IP addresses and a local DNS server.
---

# Run with a Local LLM Provider

Use Vibepit with a LLM provider like Ollama, LM Studio, or llama.cpp
on your LAN accessible from inside the sandbox while blocking all other access to your
local network.

## What is the issue?     

If you have your LLM provider (e.g. Ollama) running on a machine named 'llm-server' on your LAN,
you might need to configure vibepit using the following options to make it work.

By default, Vibepit blocks access to RFC 1918 private IP ranges and other
reserved ranges to prevent the sandbox from reaching services on your local
network. You may want to relax this only for the specific range where your LLM
provider runs, while keeping everything else blocked.

Add your IP subnet or only single IPs to the `allow-cidr` list in your global config
(`~/.config/vibepit/config.yaml`):

```yaml
allow-cidr:
  - 192.168.1.0/24 # e.g. for the full subnet
  - 192.168.1.2/32 # e.g. for a single IP
```

This allows the sandbox to reach any IP in `192.168.1.0/24` or only a single host while the default
`block-cidr` rules continue to protect all other private ranges.

!!! note

    `allow-cidr` takes precedence over `block-cidr`. If both lists contain
    overlapping ranges, the allow entry wins.

## Configure the upstream DNS resolver

If your LAN uses a local DNS server (e.g. Pi-hole, AdGuard Home, or a
corporate DNS), you can point Vibepit's DNS proxy at it by adding
`upstream-dns` to your global config:

```yaml
upstream-dns: 192.168.1.1:53
```

This routes all DNS queries through your LAN resolver after the allowlist
check, letting you resolve internal hostnames alongside public domains.


## Don't want to change upstream DNS resolver?

In this case, you can add the hosts you want to resolve using `extra-hosts`

```yaml
extra-hosts:
  - "llm-server:192.168.1.2"
```

## Example config snippet for OpenCode (~/.config/opencode/opencode.json)

```
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "llm-server:11434": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://aibox:8000/v1"
      },
```

