# Preset System Design

Replace the hardcoded allowlist presets with a data-driven preset registry
sourced from the Claude Code web default allow list. Add auto-detection of
project types and an interactive selector for first-run configuration.

## Preset Registry

All presets are defined in `proxy/presets.go` as a slice of `Preset` structs:

```go
type Preset struct {
    Name        string
    Group       string
    Description string
    Domains     []string
    Matchers    []string   // file glob patterns for auto-detection
    Includes    []string   // other preset names (for meta-presets)
}
```

- `Includes` are expanded recursively with cycle detection.
- `Matchers` are file glob patterns checked against the project root.
- `Group` controls visual grouping in the interactive selector.

### Presets

Source: https://code.claude.com/docs/en/claude-code-on-the-web.md

#### Group: Defaults

**default** (meta-preset, includes: `anthropic`, `vcs-github`)

#### Group: Package Managers

**pkg-node** -- JavaScript/Node

Matchers: `package.json`, `yarn.lock`, `pnpm-lock.yaml`, `bun.lockb`

- registry.npmjs.org
- www.npmjs.com
- www.npmjs.org
- npmjs.com
- npmjs.org
- yarnpkg.com
- registry.yarnpkg.com

**pkg-python** -- Python

Matchers: `pyproject.toml`, `requirements.txt`, `setup.py`, `Pipfile`, `poetry.lock`

- pypi.org
- www.pypi.org
- files.pythonhosted.org
- pythonhosted.org
- test.pypi.org
- pypi.python.org
- pypa.io
- www.pypa.io

**pkg-ruby** -- Ruby

Matchers: `Gemfile`, `*.gemspec`

- rubygems.org
- www.rubygems.org
- api.rubygems.org
- index.rubygems.org
- ruby-lang.org
- www.ruby-lang.org
- rubyforge.org
- www.rubyforge.org
- rubyonrails.org
- www.rubyonrails.org
- rvm.io
- get.rvm.io

**pkg-rust** -- Rust

Matchers: `Cargo.toml`

- crates.io
- www.crates.io
- index.crates.io
- static.crates.io
- rustup.rs
- static.rust-lang.org
- www.rust-lang.org

**pkg-go** -- Go

Matchers: `go.mod`

- proxy.golang.org
- sum.golang.org
- index.golang.org
- golang.org
- www.golang.org
- goproxy.io
- pkg.go.dev

**pkg-jvm** -- JVM

Matchers: `pom.xml`, `build.gradle`, `build.gradle.kts`, `build.sbt`

- maven.org
- repo.maven.org
- central.maven.org
- repo1.maven.org
- jcenter.bintray.com
- gradle.org
- www.gradle.org
- services.gradle.org
- plugins.gradle.org
- kotlin.org
- www.kotlin.org
- spring.io
- repo.spring.io

**pkg-others** -- Other Languages (PHP, .NET, Dart, Elixir, Perl, CocoaPods, Haskell, Swift)

Matchers: `composer.json`, `*.csproj`, `*.sln`, `pubspec.yaml`, `mix.exs`,
`Podfile`, `Package.swift`, `*.cabal`, `stack.yaml`

- packagist.org
- www.packagist.org
- repo.packagist.org
- nuget.org
- www.nuget.org
- api.nuget.org
- pub.dev
- api.pub.dev
- hex.pm
- www.hex.pm
- cpan.org
- www.cpan.org
- metacpan.org
- www.metacpan.org
- api.metacpan.org
- cocoapods.org
- www.cocoapods.org
- cdn.cocoapods.org
- haskell.org
- www.haskell.org
- hackage.haskell.org
- swift.org
- www.swift.org

#### Group: Infrastructure

**anthropic** -- Anthropic Services

- api.anthropic.com
- statsig.anthropic.com
- docs.claude.com
- code.claude.com
- claude.ai

**vcs-github** -- GitHub

- github.com
- www.github.com
- api.github.com
- npm.pkg.github.com
- raw.githubusercontent.com
- pkg-npm.githubusercontent.com
- objects.githubusercontent.com
- codeload.github.com
- avatars.githubusercontent.com
- camo.githubusercontent.com
- gist.github.com

**vcs-other** -- GitLab, Bitbucket

- gitlab.com
- www.gitlab.com
- registry.gitlab.com
- bitbucket.org
- www.bitbucket.org
- api.bitbucket.org

**containers** -- Container Registries

- registry-1.docker.io
- auth.docker.io
- index.docker.io
- hub.docker.com
- www.docker.com
- production.cloudflare.docker.com
- download.docker.com
- gcr.io
- *.gcr.io
- ghcr.io
- mcr.microsoft.com
- *.data.mcr.microsoft.com
- public.ecr.aws

**cloud** -- Cloud Platforms

- cloud.google.com
- accounts.google.com
- gcloud.google.com
- *.googleapis.com
- storage.googleapis.com
- compute.googleapis.com
- container.googleapis.com
- azure.com
- portal.azure.com
- microsoft.com
- www.microsoft.com
- *.microsoftonline.com
- packages.microsoft.com
- dotnet.microsoft.com
- dot.net
- visualstudio.com
- dev.azure.com
- *.amazonaws.com
- *.api.aws
- oracle.com
- www.oracle.com
- java.com
- www.java.com
- java.net
- www.java.net
- download.oracle.com
- yum.oracle.com

**linux-distros** -- Linux Distributions

- archive.ubuntu.com
- security.ubuntu.com
- ubuntu.com
- www.ubuntu.com
- *.ubuntu.com
- ppa.launchpad.net
- launchpad.net
- www.launchpad.net

**devtools** -- Development Tools & Platforms

- dl.k8s.io
- pkgs.k8s.io
- k8s.io
- www.k8s.io
- releases.hashicorp.com
- apt.releases.hashicorp.com
- rpm.releases.hashicorp.com
- archive.releases.hashicorp.com
- hashicorp.com
- www.hashicorp.com
- repo.anaconda.com
- conda.anaconda.org
- anaconda.org
- www.anaconda.com
- anaconda.com
- continuum.io
- apache.org
- www.apache.org
- archive.apache.org
- downloads.apache.org
- eclipse.org
- www.eclipse.org
- download.eclipse.org
- nodejs.org
- www.nodejs.org

**monitoring** -- Cloud Services & Monitoring

- statsig.com
- www.statsig.com
- api.statsig.com
- sentry.io
- *.sentry.io
- http-intake.logs.datadoghq.com
- *.datadoghq.com
- *.datadoghq.eu

**cdn** -- Content Delivery & Mirrors

- sourceforge.net
- *.sourceforge.net
- packagecloud.io
- *.packagecloud.io

**schema** -- Schema & Configuration

- json-schema.org
- www.json-schema.org
- json.schemastore.org
- www.schemastore.org

**mcp** -- Model Context Protocol

- *.modelcontextprotocol.io

## Auto-Detection

A new function in `config/detect.go`:

```go
func DetectPresets(projectDir string) []string
```

Iterates all presets with `Matchers` defined. For each matcher pattern, checks
if a matching file exists in the project root directory. Uses `os.Stat` for
exact names and `filepath.Glob` for glob patterns (e.g., `*.gemspec`).
Detection is top-level only -- no recursive search.

Multiple presets can match simultaneously (e.g., `go.mod` + `package.json`
activates both `pkg-go` and `pkg-node`).

## Interactive Selector

On first run (no `.vibepit/network.yaml`), `vibepit monitor` shows an
interactive terminal selector built with `charmbracelet/huh`.

Display layout:

```
Detected project: Go, Node.js

Select network presets (space to toggle, enter to confirm):

  Defaults
    [x] default (Anthropic, GitHub)

  Detected
    [x] pkg-go
    [x] pkg-node

  Package Managers
    [ ] pkg-python
    [ ] pkg-ruby
    [ ] pkg-rust
    [ ] pkg-jvm
    [ ] pkg-others

  Infrastructure
    [ ] vcs-other
    [ ] containers
    [ ] cloud
    [ ] linux-distros
    [ ] devtools
    [ ] monitoring
    [ ] cdn
    [ ] schema
    [ ] mcp
```

Behavior:

- `default` and detected presets are pre-checked but toggleable.
- "Detected" is a runtime display section, not a group. Detected presets are
  promoted from their normal group to a "Detected" heading.
- Remaining presets display under their `Group`.
- On confirm, selected preset names are written to `.vibepit/network.yaml`.

## Config Format

No structural changes to `.vibepit/network.yaml`:

```yaml
presets:
  - default
  - pkg-go
  - pkg-node

allow:
  - api.openai.com

dns-only:
  - internal.corp.example.com
```

Custom preset definitions in the global config (`~/.config/vibepit/config.yaml`)
are removed. All presets are built-in. Custom domains use the `allow` list.

## Reconfigure Flow

`vibepit --reconfigure` re-shows the interactive selector:

1. Reads existing `.vibepit/network.yaml`.
2. Pre-checks presets that are already in the saved config.
3. Re-runs detection (new marker files may have appeared).
4. Detected presets are shown as detected but only pre-checked if already saved.
5. On confirm, overwrites the `presets` list.
6. Preserves `allow` and `dns-only` entries untouched.

## Subsequent Runs

When `.vibepit/network.yaml` exists, the selector is skipped. Config is loaded
directly and presets are expanded through the registry.

Users can still add presets at runtime via `vibepit allow --preset <name>`.

## File Changes

New files:

- `proxy/presets.go` -- preset registry with all 19 presets
- `proxy/presets_test.go` -- tests for expansion, cycle detection, domain lists
- `config/detect.go` -- project type auto-detection
- `config/detect_test.go` -- detection tests

Modified files:

- `config/config.go` -- remove hardcoded preset map, use registry
- `config/setup.go` -- replace interactive flow with `huh`-based selector
- `cmd/root.go` -- add `--reconfigure` flag
- `cmd/monitor.go` -- wire up detection and selector on first run
- `go.mod` -- add `github.com/charmbracelet/huh` dependency
