// Package kitty drives the kitty terminal's remote control to open overlay
// windows over the window that runs vibepit.
package kitty

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	// ErrNotKitty means the process is not running inside a kitty window
	// with remote control enabled.
	ErrNotKitty = errors.New("not running in kitty with remote control enabled (allow_remote_control and listen_on in kitty.conf)")
	// ErrNoKitten means the kitten binary is not available.
	ErrNoKitten = errors.New("kitten binary not found in PATH")
)

// Env describes the kitty terminal hosting this process. Remote control goes
// through the socket rather than the tty because the caller typically owns
// the tty in raw mode and would swallow kitty's escape-sequence responses.
type Env struct {
	ListenOn string
	WindowID string
}

// Detect reads the remote control socket and window id from the environment
// and checks that the kitten binary is available. Both environment variables
// must be present; kitty only exports KITTY_LISTEN_ON when listen_on is
// configured. Returns ErrNotKitty or ErrNoKitten on failure.
func Detect(getenv func(string) string, lookPath func(string) (string, error)) (Env, error) {
	e := Env{
		ListenOn: getenv("KITTY_LISTEN_ON"),
		WindowID: getenv("KITTY_WINDOW_ID"),
	}
	if e.ListenOn == "" || e.WindowID == "" {
		return Env{}, ErrNotKitty
	}
	if _, err := lookPath("kitten"); err != nil {
		return Env{}, fmt.Errorf("%w: %w", ErrNoKitten, err)
	}
	return e, nil
}

// overlayVar is the kitty user variable that tags overlays we launched, so
// they can be closed again by match.
const overlayVar = "vibepit_overlay"

// overlayArgs targets our window explicitly. For launch, --match selects a
// tab, and "id:N" prefers a tab with id N over the window with id N. Tab and
// window ids are separate counters, so that lands in the wrong tab.
// window_id:N selects the tab containing our window; --next-to picks our
// window within it rather than whichever split is active.
func overlayArgs(listenOn, windowID, tag string, cmdline []string) []string {
	args := []string{
		"@", "--to", listenOn,
		"launch", "--type=overlay", "--match", "window_id:" + windowID, "--next-to", "id:" + windowID,
		"--var", overlayVar + "=" + tag, "--wait-for-child-to-exit",
		"--",
	}
	return append(args, cmdline...)
}

func closeArgs(listenOn, tag string) []string {
	return []string{"@", "--to", listenOn, "close-window", "--match", "var:" + overlayVar + "=" + tag}
}

// LaunchOverlay opens an overlay window over the kitty window this process
// runs in, executes cmdline inside it, and returns once that process exits.
// Cancelling ctx closes the overlay window too; killing the kitten client
// alone would leave it open.
func (e Env) LaunchOverlay(ctx context.Context, cmdline []string) error {
	tag := rand.Text()
	c := exec.CommandContext(ctx, "kitten", overlayArgs(e.ListenOn, e.WindowID, tag, cmdline)...)
	c.Cancel = func() error {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(cctx, "kitten", closeArgs(e.ListenOn, tag)...).Run()
		return c.Process.Kill()
	}
	// Never write to our own stdio: the caller owns the tty.
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("kitten launch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
