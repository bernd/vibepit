package wasmvt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provenance records where ghostty-vt.wasm came from.
type provenance struct {
	Commit string
	Zig    string
	SHA256 string
}

// parseProvenance parses the key=value lines of a GHOSTTY_COMMIT file.
func parseProvenance(s string) (provenance, error) {
	var p provenance
	for line := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return provenance{}, fmt.Errorf("GHOSTTY_COMMIT: malformed line %q", line)
		}
		switch k {
		case "commit":
			p.Commit = v
		case "zig":
			p.Zig = v
		case "sha256":
			p.SHA256 = v
		default:
			return provenance{}, fmt.Errorf("GHOSTTY_COMMIT: unknown key %q", k)
		}
	}
	if p.Commit == "" || p.Zig == "" || p.SHA256 == "" {
		return provenance{}, errors.New("GHOSTTY_COMMIT: missing commit, zig or sha256")
	}
	return p, nil
}

func TestParseProvenance(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    provenance
		wantErr string
	}{
		{
			name: "valid",
			in:   "commit=abc\nzig=0.16.0\nsha256=ff\n",
			want: provenance{Commit: "abc", Zig: "0.16.0", SHA256: "ff"},
		},
		{name: "missing sha256", in: "commit=abc\nzig=0.16.0\n", wantErr: "missing"},
		{name: "unknown key", in: "commit=abc\nfoo=1\n", wantErr: "unknown key"},
		{name: "malformed line", in: "commit abc\n", wantErr: "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProvenance(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestProvenance ties the committed .wasm to its source commit. It runs in
// make test, so build.yml fails on a PR whose .wasm doesn't match
// GHOSTTY_COMMIT.
func TestProvenance(t *testing.T) {
	commitFile, err := os.ReadFile("GHOSTTY_COMMIT")
	require.NoError(t, err)
	p, err := parseProvenance(string(commitFile))
	require.NoError(t, err)
	wasm, err := os.ReadFile("ghostty-vt.wasm")
	require.NoError(t, err)

	sum := sha256.Sum256(wasm)
	assert.Equal(t, hex.EncodeToString(sum[:]), p.SHA256,
		"ghostty-vt.wasm does not match GHOSTTY_COMMIT; rebuild it with make ghostty-wasm")
	assert.Len(t, p.Commit, 40)
	assert.True(t, strings.HasPrefix(p.Zig, "0.16."), "zig version %q", p.Zig)
}

// TestGeneratedCodeIsCurrent ties ghostty_vt.go to the committed .wasm and
// the wasm2go version pinned in go.mod. Translation is deterministic, so
// any difference is a stale or hand-edited file. It runs the go:generate
// line from doc.go, so the flags can't drift.
func TestGeneratedCodeIsCurrent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ghostty_vt.go")
	cmd := exec.Command("go", generateArgs(t, out)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "wasm2go: %s", stderr.String())

	want, err := os.ReadFile(out)
	require.NoError(t, err)
	got, err := os.ReadFile("ghostty_vt.go")
	require.NoError(t, err)
	// Not assert.Equal: a diff of a 4 MB file is unreadable.
	assert.True(t, bytes.Equal(want, got),
		"ghostty_vt.go is not wasm2go's output for ghostty-vt.wasm; run go generate ./vt/internal/ghostty/internal/wasmvt")
}

// generateArgs returns the arguments of doc.go's go:generate line after
// "go", with the output redirected to out.
func generateArgs(t *testing.T, out string) []string {
	t.Helper()
	src, err := os.ReadFile("doc.go")
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(src), "\n") {
		rest, ok := strings.CutPrefix(line, "//go:generate go ")
		if !ok {
			continue
		}
		args := strings.Fields(rest)
		for i := range args[:len(args)-1] {
			if args[i] == "-o" {
				args[i+1] = out
			}
		}
		return args
	}
	t.Fatal("doc.go has no go:generate line")
	return nil
}
