# Network Presets

Presets bundle commonly needed domain allowlists so you do not have to add
domains one by one. During first run, Vibepit presents an interactive selector
where you choose which presets to activate. You can re-run this selector at any
time with `vibepit run --reconfigure`. Some package-manager presets are
auto-detected from project files and pre-selected automatically.

Each preset entry below lists its name, description, auto-detection trigger (if
any), and the full set of allowed domains.

---

## Defaults

The `default` meta-preset is pre-selected on first run and includes the
following presets:

- [`anthropic`](#anthropic)
- [`cdn-github`](#cdn-github)
- [`homebrew`](#homebrew)
- [`openai`](#openai)
- [`vcs-github`](#vcs-github)

You can deselect `default` in the preset selector if you want full control over
which domains are allowed. When `default` is selected, you do not need to
enable the included presets individually.

---

## Infrastructure

### `anthropic`

Anthropic services.

```
api.anthropic.com:443
statsig.anthropic.com:443
docs.claude.com:443
code.claude.com:443
claude.ai:443
platform.claude.com:443
```

### `openai`

OpenAI services.

```
chatgpt.com:443
ab.chatgpt.com:443
auth.openai.com:443
```

### `vcs-github`

GitHub.

```
github.com:443
www.github.com:443
api.github.com:443
npm.pkg.github.com:443
raw.githubusercontent.com:443
pkg-npm.githubusercontent.com:443
objects.githubusercontent.com:443
codeload.github.com:443
avatars.githubusercontent.com:443
camo.githubusercontent.com:443
gist.github.com:443
release-assets.githubusercontent.com:443
```

### `vcs-other`

GitLab and Bitbucket.

```
gitlab.com:443
www.gitlab.com:443
registry.gitlab.com:443
bitbucket.org:443
www.bitbucket.org:443
api.bitbucket.org:443
```

### `containers`

Container registries.

```
registry-1.docker.io:443
auth.docker.io:443
index.docker.io:443
hub.docker.com:443
www.docker.com:443
production.cloudflare.docker.com:443
download.docker.com:443
gcr.io:443
*.gcr.io:443
ghcr.io:443
mcr.microsoft.com:443
*.data.mcr.microsoft.com:443
public.ecr.aws:443
```

### `cloud`

Cloud platforms (GCP, Azure, AWS, Oracle).

```
cloud.google.com:443
accounts.google.com:443
gcloud.google.com:443
*.googleapis.com:443
storage.googleapis.com:443
compute.googleapis.com:443
container.googleapis.com:443
azure.com:443
portal.azure.com:443
microsoft.com:443
www.microsoft.com:443
*.microsoftonline.com:443
packages.microsoft.com:443
dotnet.microsoft.com:443
dot.net:443
visualstudio.com:443
dev.azure.com:443
*.amazonaws.com:443
*.api.aws:443
oracle.com:443
www.oracle.com:443
java.com:443
www.java.com:443
java.net:443
www.java.net:443
download.oracle.com:443
yum.oracle.com:443
```

### `linux-distros`

Linux distribution repositories.

```
archive.ubuntu.com:443
security.ubuntu.com:443
ubuntu.com:443
www.ubuntu.com:443
*.ubuntu.com:443
ppa.launchpad.net:443
launchpad.net:443
www.launchpad.net:443
```

### `devtools`

Development tools and platforms.

```
dl.k8s.io:443
pkgs.k8s.io:443
k8s.io:443
www.k8s.io:443
releases.hashicorp.com:443
apt.releases.hashicorp.com:443
rpm.releases.hashicorp.com:443
archive.releases.hashicorp.com:443
hashicorp.com:443
www.hashicorp.com:443
repo.anaconda.com:443
conda.anaconda.org:443
anaconda.org:443
www.anaconda.com:443
anaconda.com:443
continuum.io:443
apache.org:443
www.apache.org:443
archive.apache.org:443
downloads.apache.org:443
eclipse.org:443
www.eclipse.org:443
download.eclipse.org:443
nodejs.org:443
www.nodejs.org:443
```

### `monitoring`

Monitoring and observability services.

```
statsig.com:443
www.statsig.com:443
api.statsig.com:443
sentry.io:443
*.sentry.io:443
http-intake.logs.datadoghq.com:443
*.datadoghq.com:443
*.datadoghq.eu:443
```

### `cdn`

Content delivery and mirrors.

```
sourceforge.net:443
*.sourceforge.net:443
packagecloud.io:443
*.packagecloud.io:443
```

### `schema`

Schema and configuration registries.

```
json-schema.org:443
www.json-schema.org:443
json.schemastore.org:443
www.schemastore.org:443
```

### `mcp`

Model Context Protocol.

```
*.modelcontextprotocol.io:443
```

---

## Package Managers

### `cdn-github`

GitHub Container Registry.

```
ghcr.io:443
pkg-containers.githubusercontent.com:443
```

### `homebrew`

Homebrew downloads.

```
formulae.brew.sh:443
```

### `pkg-node`

JavaScript/Node.js.

Auto-detected when your project contains: `package.json`, `yarn.lock`,
`pnpm-lock.yaml`, `bun.lockb`.

```
registry.npmjs.org:443
www.npmjs.com:443
www.npmjs.org:443
npmjs.com:443
npmjs.org:443
yarnpkg.com:443
registry.yarnpkg.com:443
```

### `pkg-python`

Python.

Auto-detected when your project contains: `pyproject.toml`,
`requirements.txt`, `setup.py`, `Pipfile`, `poetry.lock`.

```
pypi.org:443
www.pypi.org:443
files.pythonhosted.org:443
pythonhosted.org:443
test.pypi.org:443
pypi.python.org:443
pypa.io:443
www.pypa.io:443
```

### `pkg-ruby`

Ruby.

Auto-detected when your project contains: `Gemfile`, `*.gemspec`.

```
rubygems.org:443
www.rubygems.org:443
api.rubygems.org:443
index.rubygems.org:443
ruby-lang.org:443
www.ruby-lang.org:443
rubyforge.org:443
www.rubyforge.org:443
rubyonrails.org:443
www.rubyonrails.org:443
rvm.io:443
get.rvm.io:443
```

### `pkg-rust`

Rust.

Auto-detected when your project contains: `Cargo.toml`.

```
crates.io:443
www.crates.io:443
index.crates.io:443
static.crates.io:443
rustup.rs:443
static.rust-lang.org:443
www.rust-lang.org:443
```

### `pkg-go`

Go.

Auto-detected when your project contains: `go.mod`.

```
proxy.golang.org:443
sum.golang.org:443
index.golang.org:443
golang.org:443
www.golang.org:443
goproxy.io:443
pkg.go.dev:443
```

### `pkg-jvm`

JVM (Maven, Gradle, Kotlin, Spring).

Auto-detected when your project contains: `pom.xml`, `build.gradle`,
`build.gradle.kts`, `build.sbt`.

```
maven.org:443
repo.maven.org:443
central.maven.org:443
repo1.maven.org:443
jcenter.bintray.com:443
gradle.org:443
www.gradle.org:443
services.gradle.org:443
plugins.gradle.org:443
kotlin.org:443
www.kotlin.org:443
spring.io:443
repo.spring.io:443
```

### `pkg-others`

Other languages (PHP, .NET, Dart, Elixir, Perl, CocoaPods, Haskell, Swift).

Auto-detected when your project contains: `composer.json`, `*.csproj`, `*.sln`,
`pubspec.yaml`, `mix.exs`, `Podfile`, `Package.swift`, `*.cabal`, `stack.yaml`.

```
packagist.org:443
www.packagist.org:443
repo.packagist.org:443
nuget.org:443
www.nuget.org:443
api.nuget.org:443
pub.dev:443
api.pub.dev:443
hex.pm:443
www.hex.pm:443
cpan.org:443
www.cpan.org:443
metacpan.org:443
www.metacpan.org:443
api.metacpan.org:443
cocoapods.org:443
www.cocoapods.org:443
cdn.cocoapods.org:443
haskell.org:443
www.haskell.org:443
hackage.haskell.org:443
swift.org:443
www.swift.org:443
```
