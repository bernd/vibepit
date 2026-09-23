package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/kitty"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const promptFlag = "prompt"

var promptCLIFlag = &cli.BoolFlag{
	Name:  promptFlag,
	Usage: "Prompt in a kitty overlay when a connection is blocked (auto-detected; --prompt=false disables)",
}

// blockWatcher tracks the log cursor and which blocked targets have already
// been surfaced, so each domain:port triggers at most one prompt per session
// even though the agent typically retries a blocked request many times.
type blockWatcher struct {
	cursor uint64
	seen   map[string]bool
}

// Next returns the blocked entries in batch that have not been seen before
// and advances the cursor past the batch.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) []proxy.LogEntry {
	if bw.seen == nil {
		bw.seen = make(map[string]bool)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		bw.cursor = e.ID
		if e.Action != proxy.ActionBlock {
			continue
		}
		key := string(e.Source) + "/" + allowValueForEntry(e)
		if bw.seen[key] {
			continue
		}
		bw.seen[key] = true
		fresh = append(fresh, e)
	}
	return fresh
}

func (bw *blockWatcher) Cursor() uint64 { return bw.cursor }

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Errors are
// dropped: the attach loop owns the terminal, so there is nowhere to print.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt func(context.Context, proxy.LogEntry) error) {
	var bw blockWatcher
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Skip whatever was blocked before we started watching; LogsAfter(0)
	// returns a tail of history rather than "nothing yet". Retry until it
	// succeeds, otherwise the first successful poll would replay that tail.
	for {
		entries, err := client.LogsAfter(0)
		if err == nil {
			for _, e := range entries {
				bw.cursor = e.ID
			}
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := client.LogsSince(bw.Cursor())
		if err != nil {
			continue
		}
		for _, e := range bw.Next(entries) {
			// Another client may have decided already. A failed check, e.g. an
			// older proxy without /check, falls back to prompting.
			if res, err := client.Check(e); err == nil && res.Decided() {
				continue
			}
			_ = prompt(ctx, e)
		}
	}
}

// resolvePrompter decides whether to run the block prompter. An explicit
// --prompt demands a working kitty setup and fails loudly otherwise. Without
// the flag, the prompter turns on when kitty is usable and stays silent when
// it is not.
func resolvePrompter(explicit, value bool, getenv func(string) string, lookPath func(string) (string, error)) (kitty.Env, bool, error) {
	if explicit && !value {
		return kitty.Env{}, false, nil
	}
	env, err := kitty.Detect(getenv, lookPath)
	if err != nil {
		if explicit {
			return kitty.Env{}, false, fmt.Errorf("--prompt: %w", err)
		}
		return kitty.Env{}, false, nil
	}
	return env, true, nil
}

// startBlockPrompter resolves whether prompting applies to this invocation
// and, if so, starts the poller for the session returned by getSession. The
// session is resolved lazily so that a failing lookup cannot break sessions
// that do not prompt. Setup failures only abort when --prompt was given
// explicitly; auto-detected prompting is a convenience and must never stop
// run or connect from working. The returned stop function is always safe to
// call.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error)) (func(), error) {
	noop := func() {}
	explicit := cmd.IsSet(promptFlag)
	kitty, on, err := resolvePrompter(explicit, cmd.Bool(promptFlag), os.Getenv, exec.LookPath)
	if err != nil || !on {
		return noop, err
	}
	setupFailed := func(err error) (func(), error) {
		if explicit {
			return noop, fmt.Errorf("--prompt: %w", err)
		}
		tui.Status("Skipping", "kitty prompt for blocked connections: %v", err)
		return noop, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return setupFailed(err)
	}
	session, err := getSession()
	if err != nil {
		return setupFailed(err)
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return setupFailed(err)
	}

	stopLoop := startPrompterLoop(ctx, cc, pollInterval, prompterStopGrace, func(ctx context.Context, e proxy.LogEntry) error {
		return kitty.LaunchOverlay(ctx, approveCmdline(exe, session, e))
	})
	tui.Status("Watching", "blocked connections via kitty overlay")
	return func() {
		stopLoop()
		cc.Close()
	}, nil
}

// prompterStopGrace bounds how long shutdown waits for an open overlay to be
// closed. It covers kitty's close-window round trip, which LaunchOverlay
// itself caps at two seconds.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background. The returned
// stop function cancels it and waits, up to grace, for it to return, so an
// open overlay is closed before the CLI exits.
func startPrompterLoop(ctx context.Context, cc *ControlClient, interval, grace time.Duration, prompt func(context.Context, proxy.LogEntry) error) func() {
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(pctx, cc, interval, prompt)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(grace):
		}
	}
}

// sessionInfoForRunning builds the SessionInfo for an already running
// session by looking up the proxy container's published control port.
func sessionInfoForRunning(ctx context.Context, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error) {
	proxyID, err := client.FindProxyContainerID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find proxy container: %w", err)
	}
	port, err := client.FindControlPort(ctx, proxyID)
	if err != nil {
		return nil, fmt.Errorf("find control port: %w", err)
	}
	return &SessionInfo{
		ControlPort: strconv.Itoa(port),
		SessionID:   sessionID,
		ProjectDir:  projectDir,
	}, nil
}

// sanitizeText strips C0, C1, DEL, and invalid UTF-8. Domain and reason
// strings originate from the sandbox's own requests and must not be able to
// inject escape sequences into the prompt.
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}
