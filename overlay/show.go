package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Show runs model over the session and restores the screen afterwards. It
// waits for an earlier Show to finish. It returns ErrUnavailable when the
// session has no working shadow, ErrBarrierTimeout when the terminal
// didn't confirm the input switch (nothing was drawn), and ErrClosed when
// the session ends first. After the detach every path restores the screen,
// also when the program fails.
func (t *Terminal) Show(ctx context.Context, model tea.Model) error {
	t.showMu.Lock()
	defer t.showMu.Unlock()
	if err := t.detachAt(ctx); err != nil {
		return err
	}
	in, err := t.awaitBarrier(ctx)
	if err != nil {
		t.leave(false)
		return err
	}
	return t.runPrompt(ctx, in, model)
}

// awaitBarrier is T2. After barrierTimeoutsBeforeDegraded timeouts in a
// row, the session waits for input silence instead.
func (t *Terminal) awaitBarrier(ctx context.Context) (io.Reader, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-t.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	t.mu.Lock()
	degraded := t.barrierUnsupported
	t.mu.Unlock()
	var in io.Reader
	var err error
	if degraded {
		in, err = t.in.AwaitSilence(ctx, t.timing.silence)
	} else {
		in, err = t.in.AwaitBarrier(ctx, t.timing.barrierWait)
	}
	if err != nil && t.isDone() {
		err = ErrClosed
	}
	if degraded {
		return in, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case err == nil:
		t.barrierTimeouts = 0
	case errors.Is(err, ErrBarrierTimeout):
		t.barrierTimeouts++
		if t.barrierTimeouts == barrierTimeoutsBeforeDegraded {
			t.barrierUnsupported = true
			t.logf("overlay: the terminal doesn't answer DSR 5n; prompts now take input after %v without typing, so a reply or key in flight may reach the wrong side", t.timing.silence)
		}
	}
	return in, err
}

// runPrompt writes the enter sequence, runs the program and always ends
// with T3.
func (t *Terminal) runPrompt(ctx context.Context, in io.Reader, model tea.Model) (err error) {
	t.mu.Lock()
	_, _ = io.WriteString(t.cfg.Stdout, enterSeq(enterFor(t.cut)))
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	defer t.leave(true)

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// The session ending cancels the program.
		select {
		case <-t.done:
		case <-t.in.Done():
		case <-pctx.Done():
		}
		cancel()
	}()
	p := tea.NewProgram(model,
		tea.WithContext(pctx),
		tea.WithInput(in),
		tea.WithOutput(NewFilter(t.cfg.Stdout)),
		tea.WithWindowSize(cols, rows),
		tea.WithColorProfile(t.profile),
		tea.WithEnvironment(t.environ),
		tea.WithoutSignalHandler(),
	)
	t.setProgram(p)
	defer t.setProgram(nil)
	defer func() {
		// Bubble Tea recovers panics in the model; this covers its own
		// code, so the leave still runs.
		if r := recover(); r != nil {
			err = fmt.Errorf("overlay: prompt panicked: %v", r)
		}
	}()
	_, err = p.Run()
	switch {
	case t.isDone(), isClosed(t.in.Done()):
		return ErrClosed
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return err
}

func (t *Terminal) setProgram(p *tea.Program) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prog = p
}
