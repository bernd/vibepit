---
hide:
  - navigation
  - toc
---

<section class="vp-landing">
  <div class="vp-landing__copy">
    <p class="vp-landing__eyebrow">Secure Agent Sandbox</p>
    <h1>Run AI coding agents in isolated containers</h1>
    <p class="vp-landing__lead">
      Vibepit gives your coding agents a hardened runtime with filtered
      networking, runtime allowlist controls, and a monitor UI for live traffic.
    </p>
    <div class="vp-landing__actions">
      <a class="md-button md-button--primary" href="tutorials/first-sandbox/">Get Started</a>
      <a class="md-button" href="reference/cli/">CLI Reference</a>
      <a class="md-button" href="https://github.com/bernd/vibepit">GitHub</a>
    </div>
    <p class="vp-landing__meta">
      Alpha software. Expect breaking changes.
    </p>
  </div>
  <div class="vp-landing__panel">
    <img class="vp-landing__logo" src="assets/logo.png" alt="Vibepit logo">
  </div>
  <div class="vp-landing__install-wide">
    <p class="vp-landing__install-label">Two commands, and you're in the pit</p>
    <pre class="vp-landing__install"><code>curl -fsSL https://vibepit.dev/download.sh | bash
sudo mv vibepit /usr/local/bin/</code></pre>
  </div>
</section>

<h2 class="vp-home-section-title">Why Vibepit</h2>

<section class="vp-cards vp-cards--features">
  <article class="vp-card">
    <h3>Container Isolation</h3>
    <p>Each session runs in a hardened container with a read-only root filesystem and dropped capabilities.</p>
  </article>
  <article class="vp-card">
    <h3>Filtered Networking</h3>
    <p>HTTP, HTTPS, and DNS traffic is filtered through an allowlist proxy with optional network presets.</p>
  </article>
  <article class="vp-card">
    <h3>Runtime Control</h3>
    <p>Manage allowlists and inspect live traffic with CLI commands or the interactive monitor UI.</p>
  </article>
</section>

<h2 class="vp-home-section-title">Start Here</h2>

<section class="vp-cards vp-cards--guides">
  <article class="vp-card">
    <h3><a href="tutorials/first-sandbox/">First Sandbox</a></h3>
    <p>Launch your first isolated session in a project directory.</p>
  </article>
  <article class="vp-card">
    <h3><a href="how-to/allowlist-and-monitor/">Allowlist and Monitor</a></h3>
    <p>Handle live session access changes and inspect proxy logs.</p>
  </article>
  <article class="vp-card">
    <h3><a href="reference/cli/">CLI Reference</a></h3>
    <p>Review command syntax, flags, and operational behavior.</p>
  </article>
  <article class="vp-card">
    <h3><a href="how-to/ai-coding-agents/">AI Coding Agents</a></h3>
    <p>Set up Claude Code, Cursor, or Copilot with the right network presets.</p>
  </article>
  <article class="vp-card">
    <h3><a href="explanations/architecture/">Architecture</a></h3>
    <p>How the proxy, dev container, and isolated network fit together.</p>
  </article>
  <article class="vp-card">
    <h3><a href="explanations/security-model/">Security Model</a></h3>
    <p>Understand assumptions, boundaries, and defense-in-depth controls.</p>
  </article>
</section>

!!! warning
    Vibepit is in alpha. Expect breaking changes.

!!! warning
    Vibepit adds defense in depth, but container isolation is not equal to VM isolation.
