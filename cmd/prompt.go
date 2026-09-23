package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

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
	seen   map[proxy.Target]bool
}

// Next returns the blocked entries in batch that have not been seen before
// and advances the cursor past the batch.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) []proxy.LogEntry {
	if bw.seen == nil {
		bw.seen = make(map[proxy.Target]bool)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		bw.cursor = e.ID
		if e.Action != proxy.ActionBlock {
			continue
		}
		key := e.Target()
		if bw.seen[key] {
			continue
		}
		bw.seen[key] = true
		fresh = append(fresh, e)
	}
	return fresh
}

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Errors are
// dropped: the attach loop owns the terminal, so there is nowhere to print.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt func(context.Context, proxy.LogEntry) error) {
	var bw blockWatcher
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Skip whatever was blocked before we started watching: start the
	// cursor at the newest entry. Retry until that succeeds, otherwise the
	// first poll would replay old blocks.
	for {
		entries, err := client.Logs()
		if err == nil {
			if len(entries) > 0 {
				bw.cursor = entries[len(entries)-1].ID
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
		entries, err := client.LogsAfter(bw.cursor)
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

// startBlockPrompter starts the kitty prompter for the session returned by
// getSession. The session is resolved lazily so that a failing lookup cannot
// break sessions that do not prompt. --prompt=false turns it off. An explicit
// --prompt makes every setup failure fatal. Otherwise prompting is a
// convenience that must never stop run or connect: outside kitty it stays
// quiet, and a failure inside kitty only prints a warning. The returned stop
// function is always safe to call.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error)) (func(), error) {
	noop := func() {}
	explicit := cmd.IsSet(promptFlag)
	if explicit && !cmd.Bool(promptFlag) {
		return noop, nil
	}
	stop, err := setupBlockPrompter(ctx, getSession)
	switch {
	case err == nil:
		tui.Status("Watching", "blocked connections via kitty overlay")
		return stop, nil
	case explicit:
		return noop, fmt.Errorf("--prompt: %w", err)
	case errors.Is(err, kitty.ErrNotKitty), errors.Is(err, kitty.ErrNoKitten):
		return noop, nil
	default:
		tui.Status("Skipping", "kitty prompt for blocked connections: %v", err)
		return noop, nil
	}
}

func setupBlockPrompter(ctx context.Context, getSession func() (*SessionInfo, error)) (func(), error) {
	env, err := kitty.Detect(os.Getenv, exec.LookPath)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	session, err := getSession()
	if err != nil {
		return nil, err
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return nil, err
	}
	stopLoop := startPrompterLoop(ctx, cc, pollInterval, prompterStopGrace, func(ctx context.Context, e proxy.LogEntry) error {
		return env.LaunchOverlay(ctx, approveCmdline(exe, session, e))
	})
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
	return newSessionInfo(strconv.Itoa(port), sessionID, projectDir), nil
}
