package overlay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	// maxBuffered is how much output is held while paused. Beyond that the
	// pump stops reading and the session's writes stall.
	maxBuffered = 1 << 20
	// pauseTimeout bounds how long Pause waits for a clean cut point. An
	// app killed inside a synchronized-output batch or a string never
	// finishes it; terminals give up on such a batch within about a second
	// too. Past this, Pause cuts anyway.
	pauseTimeout = time.Second
	// pausePoll re-checks for a clean point while no output arrives, for
	// conditions that clear with time, like a fresh cursor save.
	pausePoll = 10 * time.Millisecond
)

// cutState is the stream state where the overlay interrupts the output.
type cutState struct {
	termModes
	// Aborted is set when the stream was cut inside an escape sequence
	// after pauseTimeout. The overlay aborts it with CAN before drawing;
	// what the app sends after that of the sequence shows as text.
	Aborted bool
}

// outputMux owns the session's output stream. Every chunk passes through
// the tracker, then goes to dst, or to a buffer while paused.
type outputMux struct {
	src          io.Reader
	dst          io.Writer
	tracker      *modeTracker
	pauseTimeout time.Duration

	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
	// pending is non-nil while a Pause waits for a cut point. The pump
	// closes it after storing the modes at the cut.
	pending chan struct{}
	cutAt   cutState
	buf     bytes.Buffer
	done    bool
}

// newOutputMux returns a mux copying src to dst through tracker.
func newOutputMux(src io.Reader, dst io.Writer, tracker *modeTracker) *outputMux {
	m := &outputMux{src: src, dst: dst, tracker: tracker, pauseTimeout: pauseTimeout}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// pump copies src until it ends or a write to dst fails.
func (m *outputMux) pump() error {
	defer func() {
		m.mu.Lock()
		m.done = true
		// No more output: the stream ends here, whatever state it is in.
		if m.pending != nil {
			m.cut(false)
		}
		m.mu.Unlock()
	}()
	buf := make([]byte, chunkSize)
	for {
		n, rerr := m.src.Read(buf)
		if n > 0 {
			if err := m.deliver(buf[:n]); err != nil {
				return err
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// deliver feeds and routes one chunk. The lock is held across feeding and
// the write to dst, so the tracker state under the lock always matches what
// dst has seen, and once Pause returns no more session bytes reach dst.
func (m *outputMux) deliver(p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tracker.Feed(p)
	if m.paused {
		m.buf.Write(p)
		for m.paused && m.buf.Len() >= maxBuffered {
			m.cond.Wait()
		}
		return nil
	}
	if _, err := m.dst.Write(p); err != nil {
		return err
	}
	if m.pending != nil && m.tracker.canCut() {
		m.cut(false)
	}
	return nil
}

// cut switches to buffering, records the state at this point, and wakes
// the waiting Pause. A forced cut also brings the tracker in line with what
// the overlay does to an unclean point. Caller holds mu.
func (m *outputMux) cut(force bool) {
	if force {
		m.cutAt.termModes, m.cutAt.Aborted = m.tracker.forceCut()
	} else {
		m.cutAt = cutState{termModes: m.tracker.Snapshot()}
	}
	m.paused = true
	close(m.pending)
	m.pending = nil
}

// Pause diverts output into a buffer and returns the stream state at the
// cut. It cuts at a chunk boundary in ground state, outside a synchronized-
// output batch and a fresh cursor save. Apps write whole sequences, so such
// a point normally comes within milliseconds. After pauseTimeout it cuts
// anyway, so an app that died mid-sequence cannot block prompts for good.
//
// There is one caller at a time: Terminal.Show serializes overlays. The
// state is captured under the same lock that feeds the tracker, so bytes
// buffered after the cut are not part of it. resume replays the
// buffer to dst and resumes passing output through.
func (m *outputMux) Pause(ctx context.Context) (cutState, func() error, error) {
	m.mu.Lock()
	ready := make(chan struct{})
	m.pending = ready
	if m.done || m.tracker.canCut() {
		m.cut(false)
	}
	m.mu.Unlock()

	deadline := time.NewTimer(m.pauseTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(pausePoll)
	defer poll.Stop()
	for {
		select {
		case <-ready:
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.cutAt, sync.OnceValue(m.resume), nil
		case <-poll.C:
			m.tryCut(false)
		case <-deadline.C:
			m.tryCut(true)
		case <-ctx.Done():
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.pending != nil {
				m.pending = nil
				return cutState{}, nil, ctx.Err()
			}
			// Lost the race to the pump: already paused.
			return m.cutAt, sync.OnceValue(m.resume), nil
		}
	}
}

// tryCut cuts for a waiting Pause, at a clean point unless forced.
func (m *outputMux) tryCut(force bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && (force || m.tracker.canCut()) {
		m.cut(force)
	}
}

func (m *outputMux) resume() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.paused {
		return nil
	}
	var err error
	if m.buf.Len() > 0 {
		_, err = m.dst.Write(m.buf.Bytes())
	}
	m.buf.Reset()
	if m.buf.Cap() > 64<<10 {
		// Do not hold on to the memory of one busy prompt for the session.
		m.buf = bytes.Buffer{}
	}
	m.paused = false
	m.cond.Broadcast()
	return err
}
