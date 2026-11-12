package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const promptFlag = "prompt"

// promptCLIFlag returns a new flag each time: a parsed flag remembers that
// it was set, and startBlockPrompter depends on telling unset from false.
func promptCLIFlag() cli.Flag {
	return &cli.BoolFlag{
		Name:  promptFlag,
		Usage: "Show an allow/deny prompt over the session when the proxy blocks a connection (default: the \"prompt\" key of the global config)",
	}
}

// promptFunc asks the user about the queued targets. The prompt posts the
// decisions to the control API itself and removes decided targets from the
// queue. It reports the targets the user dismissed without a decision.
type promptFunc func(ctx context.Context, q *promptQueue) (promptResult, error)

// promptResult is how a prompt ended.
type promptResult struct {
	// Dismissed holds the targets that were queued when the user dismissed
	// the prompt. It is empty when the prompt closed on an empty queue:
	// targets queued after that are still to be shown.
	Dismissed []proxy.Target
}

// blockWatcher tracks which blocked targets have already been surfaced, so
// each domain:port triggers at most one prompt per session even though the
// agent typically retries a blocked request many times.
type blockWatcher struct {
	// asked maps every target seen to whether it was asked about already
	// and isn't to be asked again.
	asked map[proxy.Target]bool
}

const (
	// maxPromptFailures bounds how many prompts in a row may fail before
	// prompting stops. Stopping closes the event stream, so the proxy stops
	// holding requests for prompts that won't show. Barrier timeouts don't
	// count, see promptLoop.settle.
	maxPromptFailures = 5
	// maxPromptTargets bounds the targets a session tracks. The sandbox
	// picks their names, so past it prompting stops rather than holding
	// more: the targets stay blocked, as without a prompt.
	maxPromptTargets = 1000
)

var errNoEventStream = errors.New("the proxy predates prompts; restart the session to get them")

var errTooManyTargets = fmt.Errorf("more than %d blocked targets, no more prompts in this session", maxPromptTargets)

var errPromptsFailing = fmt.Errorf("%d prompts in a row failed, no more prompts in this session", maxPromptFailures)

// Next returns the entries in batch that allowing would unblock and that
// have not been seen before. It fails with errTooManyTargets at a target
// past maxPromptTargets.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) ([]proxy.LogEntry, error) {
	if bw.asked == nil {
		bw.asked = make(map[proxy.Target]bool)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		if !e.Allowable() {
			continue
		}
		key := e.Target()
		asked, known := bw.asked[key]
		if !known && len(bw.asked) >= maxPromptTargets {
			return fresh, errTooManyTargets
		}
		if asked {
			continue
		}
		bw.asked[key] = true
		fresh = append(fresh, e)
	}
	return fresh, nil
}

// Forget lets target prompt again on its next block. It still counts
// toward maxPromptTargets.
func (bw *blockWatcher) Forget(target proxy.Target) {
	if _, ok := bw.asked[target]; ok {
		bw.asked[target] = false
	}
}

// runBlockPrompter follows the proxy's event stream and queues every unseen
// target that allowing would unblock. When the queue has targets and no
// prompt shows, it calls prompt with the queue, which keeps filling while
// the prompt shows, until ctx is done. Feed errors are retried by the feed;
// prompt failures are for the prompt function to report, see loggedPrompt.
// It returns early, with the reason, once no more prompts can be shown:
// they are unavailable, keep failing, too many targets were blocked, or
// the proxy predates the event stream.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt promptFunc) error {
	// An open stream makes the proxy hold requests, so it must not outlive
	// the prompts.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &promptLoop{client: client, q: newPromptQueue()}
	// Tail 0 skips whatever was blocked before we started watching.
	feed := startFeed(ctx, client, 0, interval)
	type ended struct {
		res promptResult
		err error
	}
	var (
		shown    chan ended // the open prompt's result; nil while none shows
		limitErr error
	)
	for {
		if shown == nil {
			if p.q.Len() > 0 && ctx.Err() == nil {
				shown = make(chan ended, 1)
				go func(done chan<- ended) {
					res, err := prompt(ctx, p.q)
					done <- ended{res, err}
				}(shown)
			} else if limitErr != nil {
				return limitErr
			}
		}
		select {
		case b, ok := <-feed:
			if !ok {
				// ctx is done. Let an open prompt restore the screen first.
				if shown != nil {
					<-shown
				}
				return nil
			}
			if errors.Is(b.Err, ErrUnsupportedEndpoint) {
				// The feed delivers nothing after this, so nothing is queued.
				return errNoEventStream
			}
			if b.Reset {
				p.q.DropHolds()
			}
			if err := p.queue(b.Events); err != nil {
				limitErr = err
			}
		case e := <-shown:
			shown = nil
			if err := p.settle(e.res, e.err); err != nil {
				return err
			}
		}
	}
}

// promptLoop is the state runBlockPrompter keeps between prompts. Only its
// goroutine adds to q; the open prompt removes what it decides.
type promptLoop struct {
	bw     blockWatcher
	client *ControlClient
	q      *promptQueue
	// failures counts the prompts that failed since the last one that
	// didn't, see settle.
	failures int
}

// queue adds the targets in events that should be asked about to q. It
// fails with errTooManyTargets once a new target is past maxPromptTargets.
func (p *promptLoop) queue(events []proxy.Event) error {
	var limitErr error
	for _, ev := range events {
		switch ev.Type {
		case proxy.EventHoldEnd:
			p.q.EndHold(ev.HoldEnd)
			continue
		case proxy.EventLog, proxy.EventHoldOpen:
		default:
			continue
		}
		e := ev.Log
		// A held request's block only ends a hold that brought the target
		// here already, or that the user dismissed: asking again takes a
		// new request.
		if !e.Allowable() || e.HoldID != 0 {
			continue
		}
		// A queued target is being asked about already: a new hold only
		// restarts its countdown. A block changes nothing, and re-adding it
		// could bring back a target the prompt just decided.
		if p.q.Has(e.Target()) {
			if e.Action == proxy.ActionHold {
				p.q.Add(e)
			}
			continue
		}
		fresh, err := p.bw.Next([]proxy.LogEntry{e})
		if err != nil {
			limitErr = err
		}
		for _, e := range fresh {
			// Another client may have decided already. An open hold needs no
			// check: the proxy opened it on finding the target undecided, and
			// a decision since ends it, which takes it out of q again. A
			// failed check, e.g. an older proxy without /check, falls back to
			// prompting.
			if e.Action != proxy.ActionHold || e.Outcome != proxy.HoldNone {
				if res, err := p.client.Check(e); err == nil && res.Decided() {
					continue
				}
			}
			p.q.Add(e)
		}
	}
	return limitErr
}

// settle updates bw and q for how the prompt ended. A dismissal is not a
// decision: the dismissed targets leave q and may be asked about again on
// their next hold or block. Targets queued after the dismissal, or after
// the prompt closed on an empty queue, stay for the next prompt. A failed
// prompt decided nothing either: all its targets leave q and may be asked
// about again. Without a shadow no prompt can be shown, now or later, and
// after maxPromptFailures failed prompts in a row none is expected to:
// then an error is returned.
func (p *promptLoop) settle(res promptResult, err error) error {
	if errors.Is(err, overlay.ErrUnavailable) {
		return err
	}
	if err == nil {
		p.failures = 0
		for _, t := range res.Dismissed {
			p.q.Remove(t)
			p.bw.Forget(t)
		}
		return nil
	}
	for _, it := range p.q.Clear() {
		p.bw.Forget(it.Entry.Target())
	}
	// Barrier timeouts and a busy session showed nothing, and aren't the
	// prompt's fault: after two timeouts in a row the terminal takes input
	// after a pause in typing, which can't time out, and a busy session
	// reads its input again.
	if errors.Is(err, overlay.ErrBarrierTimeout) || errors.Is(err, overlay.ErrSessionBusy) {
		return nil
	}
	p.failures++
	if p.failures >= maxPromptFailures {
		return errPromptsFailing
	}
	return nil
}

// prompterStopGrace bounds how long shutdown waits for an open prompt to
// restore the screen.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background and notes why
// it stopped early to logf. The returned stop function cancels it and waits,
// up to prompterStopGrace, for it to return, so an open prompt is closed
// before the CLI exits.
func startPrompterLoop(ctx context.Context, cc *ControlClient, prompt promptFunc, logf func(format string, args ...any)) func() {
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// loggedPrompt noted the prompt that found prompts unavailable.
		if err := runBlockPrompter(pctx, cc, pollInterval, prompt); err != nil && !errors.Is(err, overlay.ErrUnavailable) {
			logf("prompts stopped: %v", err)
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(prompterStopGrace):
		}
	}
}

// blockPrompter is the prompting set up for one attach: the hooks that hand
// it the session's terminal, and a stop function.
type blockPrompter struct {
	hooks ctr.TerminalHooks // start prompting on the session's terminal; zero when prompting is off
	stop  func()            // ends prompting and closes an open prompt; always safe to call
	notes *promptNotes      // what prompting couldn't show; nil when prompting is off
}

// report prints the notes collected during the session. Call it after stop,
// once the session gave the terminal back.
func (bp *blockPrompter) report() {
	for _, line := range bp.notes.Lines() {
		tui.Warn("%s", line)
	}
}

var noBlockPrompter = &blockPrompter{stop: func() {}}

// startBlockPrompter sets up prompting when the --prompt flag or the global
// config asks for it. The session and the control client are resolved
// before the attach takes over the terminal, so a failure prints normally.
// Prompting starts once the attach hands over the session's terminal.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	return startBlockPrompterFrom(ctx, cmd, config.DefaultGlobalPath(), tui.Warn, getSession)
}

// startBlockPrompterFrom is startBlockPrompter with the global config file
// and the warning output passed in.
//
// The flag wins when given, also as --prompt=false, and the config isn't
// read then. A prompt the flag asks for has to work, so a failure stops
// the command. One that only the config asks for is a default for every
// session: a failure costs the prompt and a warning, never the attach,
// which needs neither the config nor the control API. That covers connect
// and run attaching to a running session; a run that starts one has loaded
// the config by now and stopped on a broken one.
func startBlockPrompterFrom(ctx context.Context, cmd *cli.Command, globalPath string, warnf func(format string, args ...any), getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	if cmd.IsSet(promptFlag) {
		if !cmd.Bool(promptFlag) {
			return noBlockPrompter, nil
		}
		bp, err := newBlockPrompter(ctx, getSession)
		if err != nil {
			return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
		}
		return bp, nil
	}
	global, err := config.LoadGlobal(globalPath)
	if err != nil {
		warnf("continuing without the blocked connection prompt: cannot read its setting from %s: %v", globalPath, err)
		return noBlockPrompter, nil
	}
	if !global.Prompt {
		return noBlockPrompter, nil
	}
	bp, err := newBlockPrompter(ctx, getSession)
	if err != nil {
		warnf("continuing without the blocked connection prompt set in %s: %v", globalPath, err)
		return noBlockPrompter, nil
	}
	return bp, nil
}

// newBlockPrompter resolves the session and its control client and returns
// the prompting for one attach.
func newBlockPrompter(ctx context.Context, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	session, err := getSession()
	if err != nil {
		return nil, err
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return nil, err
	}
	notes := &promptNotes{}

	var (
		mu       sync.Mutex
		stopped  bool
		stopLoop func()
	)
	onTerminal := func(t *overlay.Terminal) {
		mu.Lock()
		defer mu.Unlock()
		if stopped || stopLoop != nil {
			return
		}
		// show puts the approve screen over the session. Show returns once
		// the screen's program has stopped, so its result is settled.
		show := func(ctx context.Context, q *promptQueue) (promptResult, error) {
			header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
			screen := newApproveScreen(session, cc, q)
			err := t.Show(ctx, tui.NewWindow(header, screen))
			return screen.result(), err
		}
		stopLoop = startPrompterLoop(ctx, cc, loggedPrompt(notes.Printf, show), notes.Printf)
	}
	return &blockPrompter{
		hooks: ctr.TerminalHooks{OnTerminal: onTerminal, Logf: notes.Printf},
		notes: notes,
		stop: func() {
			mu.Lock()
			stopped = true
			s := stopLoop
			mu.Unlock()
			if s != nil {
				s()
			}
			cc.Close()
		},
	}, nil
}

// loggedPrompt notes why a prompt failed or wasn't shown: the session owns
// the terminal, so it's reported after the session. A prompt ended by the
// session ending isn't a failure.
func loggedPrompt(logf func(format string, args ...any), prompt promptFunc) promptFunc {
	return func(ctx context.Context, q *promptQueue) (promptResult, error) {
		res, err := prompt(ctx, q)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			if items := q.Items(); len(items) > 0 {
				logf("prompt for %s: %v", items[0].Entry.Target(), err)
			} else {
				logf("prompt: %v", err)
			}
		}
		return res, err
	}
}

// sessionInfoForRunning builds the SessionInfo for an already running
// session from the proxy container's published control port.
func sessionInfoForRunning(ctx context.Context, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error) {
	proxyID, err := client.FindProxyContainerID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find proxy container: %w", err)
	}
	port, err := client.FindControlPort(ctx, proxyID)
	if err != nil {
		return nil, fmt.Errorf("find control port: %w", err)
	}
	return &SessionInfo{ControlPort: strconv.Itoa(port), SessionID: sessionID, ProjectDir: projectDir}, nil
}
