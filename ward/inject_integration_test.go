//go:build integration

package ward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

const injectReproEnv = "WARD_INJECT_REPRO"

// The child prints frames of wide box-drawing lines, larger than one PTY
// read, so read boundaries fall inside multi-byte characters.
func TestInjectReproChild(t *testing.T) {
	if os.Getenv(injectReproEnv) != "child" {
		t.Skip("helper process")
	}
	line := strings.Repeat("─", 300)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var sb strings.Builder
		for range 40 {
			sb.WriteString("\x1b[38;5;240m" + line + "\x1b[0m\r\n")
		}
		sb.WriteString("\x1b[J")
		os.Stdout.WriteString(sb.String())
		time.Sleep(5 * time.Millisecond)
	}
}

// The host runs the child under ward while status updates arrive faster
// than frames, so bar writes land between PTY reads.
func TestInjectReproHost(t *testing.T) {
	if os.Getenv(injectReproEnv) != "host" {
		t.Skip("helper process")
	}
	statusCh := make(chan StatusUpdate, 1)
	go func() {
		for i := 0; ; i++ {
			statusCh <- StatusUpdate{Message: fmt.Sprintf("status %d", i)}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	w := NewWrapper(Options{
		Command: []string{os.Args[0], "-test.run", "TestInjectReproChild"},
		Env:     []string{injectReproEnv + "=child"},
		Status:  statusCh,
	})
	code, err := w.Run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// TestWrapperNeverSplitsChildOutput runs ward on a real PTY and checks
// that no bar write lands inside a character or an escape sequence of
// the child. Before the output gate, a run of this size produced 10-15
// split characters.
func TestWrapperNeverSplitsChildOutput(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run", "TestInjectReproHost")
	cmd.Env = append(os.Environ(), injectReproEnv+"=host")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	require.NoError(t, err)
	defer ptmx.Close() //nolint:errcheck
	out, _ := io.ReadAll(ptmx)
	_ = cmd.Wait()
	require.Greater(t, len(out), 1<<20, "child output should span many PTY reads")

	const inject = "\x1b7\x1b[" // every ward write starts with DECSC + CSI
	var splits []string
	injections := 0
	for idx := 0; ; {
		p := bytes.Index(out[idx:], []byte(inject))
		if p < 0 {
			break
		}
		pos := idx + p
		injections++
		before := out[max(pos-64, 0):pos]
		if len(before) > 0 && !utf8.FullRune(lastRuneStart(before)) {
			splits = append(splits, fmt.Sprintf("inside character at %d: %q", pos, out[max(pos-4, 0):min(pos+12, len(out))]))
		}
		if insideCSI(before) {
			splits = append(splits, fmt.Sprintf("inside CSI at %d: %q", pos, out[max(pos-16, 0):min(pos+12, len(out))]))
		}
		idx = pos + len(inject)
	}
	require.Greater(t, injections, 50, "status updates should have produced bar writes")
	require.Empty(t, splits)
}

// lastRuneStart returns the tail of b from the start of its last rune;
// b must not be empty.
func lastRuneStart(b []byte) []byte {
	i := len(b) - 1
	for i > 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	return b[i:]
}

// insideCSI reports whether b ends inside a control sequence, a lone
// trailing ESC included.
func insideCSI(b []byte) bool {
	e := bytes.LastIndexByte(b, 0x1b)
	if e < 0 {
		return false
	}
	if e == len(b)-1 {
		return true
	}
	if b[e+1] != '[' {
		return false
	}
	for _, c := range b[e+2:] {
		if c >= csiFinalMin && c <= csiFinalMax {
			return false
		}
	}
	return true
}
