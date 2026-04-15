module github.com/bernd/vibepit

go 1.26.0

require (
	charm.land/bubbletea/v2 v2.0.2
	charm.land/lipgloss/v2 v2.0.2
	github.com/adrg/xdg v0.5.3
	github.com/charmbracelet/colorprofile v0.4.3
	github.com/charmbracelet/ssh v0.0.0-20250826160808-ebfa259c7309
	github.com/charmbracelet/x/ansi v0.11.6
	github.com/creack/pty v1.1.24
	github.com/docker/docker v28.5.2+incompatible
	github.com/docker/go-connections v0.6.0
	github.com/elazarl/goproxy v1.8.2
	github.com/knadh/koanf/parsers/yaml v1.1.0
	github.com/knadh/koanf/providers/file v1.2.1
	github.com/knadh/koanf/v2 v2.3.3
	github.com/miekg/dns v1.1.72
	github.com/rs/xid v1.6.0
	github.com/stretchr/testify v1.11.1
	github.com/urfave/cli/v3 v3.7.0
	golang.org/x/crypto v0.49.0
	golang.org/x/term v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Azure/go-ansiterm v0.0.0-20250102033503-faa5f7b0171c // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260309091805-903bfd0cf188 // indirect
	github.com/charmbracelet/x/conpty v0.1.0 // indirect
	github.com/charmbracelet/x/errors v0.0.0-20240508181413-e8d8b6e2de86 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-runewidth v0.0.21 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/sys/atomicwriter v0.1.0 // indirect
	github.com/moby/term v0.5.2 // indirect
	github.com/morikuni/aec v1.1.0 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/unixshells/vt-go v0.2.0
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.67.0 // indirect
	go.opentelemetry.io/otel v1.42.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.39.0 // indirect
	go.opentelemetry.io/otel/metric v1.42.0 // indirect
	go.opentelemetry.io/otel/trace v1.42.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/mod v0.34.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.43.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)

replace github.com/unixshells/vt-go v0.2.0 => ./internal/vt-go
