package overlay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	chunkSize = 4096
	// inputQuiet is how long Acquire waits for a gap in the input before
	// switching sinks, so a key sequence arriving in two reads is less
	// likely to be split between the session and the overlay.
	inputQuiet = 10 * time.Millisecond
	// inputQuietMax bounds the wait for that gap.
	inputQuietMax = 200 * time.Millisecond
	// escTimeout is how long an escape sequence left unfinished at the end
	// of a read waits for the rest. A reply split by a slow link completes;
	// a lone Esc key does not, and goes to the overlay after this.
	escTimeout = 100 * time.Millisecond
	// maxTracked bounds an unfinished sequence the mux remembers while no
	// overlay holds the input. A longer one is not tracked: its rest may
	// reach an overlay acquired meanwhile.
	maxTracked = 64 << 10
	// sessionQueueMax is how much input may wait for the session before the
	// pump waits too, as a direct copy would. While an overlay holds the
	// input the pump never waits: only the terminal's replies go to the
	// session then, and the overlay needs its keys.
	sessionQueueMax = 64 << 10
	// overlayInputMax bounds input waiting for the overlay program. The
	// program reads continuously; once it stopped, what waits goes to the
	// session on release. Past the bound, input is dropped.
	overlayInputMax = 1 << 20
)

// inputMux owns the reading side of the user's terminal for the life of a
// session. Input goes to the session, except while an overlay holds it
// through Acquire: then the overlay gets the user's input, and the session
// still gets what the terminal sends it on its own, replies to its queries
// and focus reports, which may arrive while an overlay shows.
//
// A sequence goes whole to where its first byte went, even when a read
// splits it and the sink changes in between.
//
// All writes are made under mu and never block while an overlay holds the
// input, so input reaches each side in order and the overlay gets its keys
// even when the session stopped reading.
type inputMux struct {
	src     io.Reader
	session *queueWriter
	// cprReply, if set, tells whether a CSI … R now is the reply to a
	// cursor position query rather than a key, and counts the reply.
	cprReply func() bool

	mu        sync.Mutex
	sink      *overlayInput // the overlay's input, nil when none holds it
	lastInput time.Time
	done      bool // src ended, no further input

	// partial is the escape sequence left unfinished by the last read, to
	// be completed by the next. Its first owed bytes already went to the
	// session. While no overlay holds the input, all of it is passed on at
	// once and only remembered. While one does, the rest is held until the
	// next read or partialTimer.
	partial       []byte
	owed          int
	partialReport bool
	partialAt     time.Time
	partialTimer  *time.Timer
}

// newInputMux returns a mux that copies src to session until acquired.
func newInputMux(src io.Reader, session io.Writer) *inputMux {
	return &inputMux{src: src, session: newQueueWriter(session, sessionQueueMax)}
}

// pump copies src until it ends or a write to the session fails. Input
// still queued for the session may be pending when it returns, see
// closeSession.
func (m *inputMux) pump() error {
	buf := make([]byte, chunkSize)
	for {
		n, rerr := m.src.Read(buf)
		if n > 0 {
			if err := m.deliver(buf[:n]); err != nil {
				m.finish(err)
				return err
			}
		}
		if rerr != nil {
			m.finish(io.EOF)
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// closeSession waits until the queued input reached the session. It blocks
// as long as the session does not read.
func (m *inputMux) closeSession() error { return m.session.Close() }

// deliver routes one chunk.
func (m *inputMux) deliver(p []byte) error {
	m.mu.Lock()
	now := time.Now()
	m.lastInput = now
	owed := 0
	if m.partial != nil {
		if m.sink == nil && now.Sub(m.partialAt) >= escTimeout {
			// Not continued in time: it was a key, like a lone Esc.
			m.takePartial()
		} else {
			owed = m.owed
			p = append(m.takePartial(), p...)
		}
	}

	if m.sink == nil {
		rest, report := splitInput(p, m.cprReply, func([]byte, unitKind) {})
		if len(rest) > 0 && len(rest) <= maxTracked {
			m.partial = append([]byte(nil), rest...)
			m.owed, m.partialReport, m.partialAt = len(rest), report, now
		}
		m.mu.Unlock()
		// Outside mu: the queue may make the pump wait for the session,
		// and Acquire must still get through to stop that.
		_, err := m.session.Write(p[owed:])
		return err
	}
	defer m.mu.Unlock()

	var toSession, toOwner []byte
	rest, restReport := splitInput(p, m.cprReply, func(unit []byte, kind unitKind) {
		switch {
		case owed > 0:
			// Begun before the overlay took the input: its start went to
			// the session, and so does the rest.
			toSession = append(toSession, unit[min(owed, len(unit)):]...)
			owed = 0
		case kind == unitReport:
			toSession = append(toSession, unit...)
		default:
			toOwner = append(toOwner, unit...)
		}
	})
	if len(rest) > 0 {
		m.hold(rest, owed, restReport, escTimeout)
	}
	m.sink.write(toOwner)
	return m.session.push(toSession)
}

// hold keeps an unfinished escape sequence for the next read. If none comes
// within d, it is a key, or the start of a reply that got stuck, and is
// passed on as it stands. Caller holds mu.
func (m *inputMux) hold(rest []byte, owed int, report bool, d time.Duration) {
	m.partial = append([]byte(nil), rest...)
	m.owed, m.partialReport, m.partialAt = owed, report, time.Now()
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.partialTimer == t {
			m.flushPartial()
		}
	})
	m.partialTimer = t
}

// flushPartial passes on the held sequence as it stands: to the session if
// it started there, is a reply, or no overlay holds the input any more.
// Caller holds mu.
func (m *inputMux) flushPartial() {
	owed, report := m.owed, m.partialReport
	p := m.takePartial()
	if m.sink == nil || owed > 0 || report {
		_ = m.session.push(p[min(owed, len(p)):])
		return
	}
	m.sink.write(p)
}

// takePartial returns and forgets the held sequence. Caller holds mu.
func (m *inputMux) takePartial() []byte {
	if m.partialTimer != nil {
		m.partialTimer.Stop()
	}
	p := m.partial
	m.partial, m.partialTimer = nil, nil
	return p
}

func (m *inputMux) finish(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = true
	if m.sink != nil {
		m.sink.end(err)
	}
}

// Acquire routes the user's input to the returned reader until release is
// called. Terminal.Show makes sure there is one holder at a time. Before
// switching, it waits briefly for a gap in the input so an in-flight key
// sequence reaches one side whole. Release is idempotent. Input the reader
// has not handed out when released goes to the session.
func (m *inputMux) Acquire(ctx context.Context) (*overlayInput, func(), error) {
	deadline := time.Now().Add(inputQuietMax)
	for {
		m.mu.Lock()
		quiet := time.Since(m.lastInput)
		m.mu.Unlock()
		if quiet >= inputQuiet || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-time.After(inputQuiet - quiet):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}

	in := newOverlayInput()
	m.mu.Lock()
	if m.done {
		in.end(io.EOF)
	} else {
		m.sink = in
		m.session.setNoWait(true)
		// A sequence the session got the start of gets its rest, if that
		// is still on its way.
		if m.partial != nil {
			if left := escTimeout - time.Since(m.partialAt); left > 0 {
				m.hold(m.partial, m.owed, m.partialReport, left)
			} else {
				m.takePartial()
			}
		}
	}
	m.mu.Unlock()

	release := sync.OnceFunc(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		unread := in.detach()
		if m.sink != in {
			return
		}
		m.sink = nil
		m.session.setNoWait(false)
		// Typed after the overlay stopped reading: meant for the session.
		_ = m.session.push(unread)
		if m.partial != nil {
			m.flushPartial()
		}
	})
	return in, release, nil
}

// overlayInput is the overlay program's input. Writes never block, so the
// mux can deliver under its lock whether or not the program reads.
type overlayInput struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	err     error // returned once buf is drained
	stopped bool  // the program is done: reads wait for detach
}

func newOverlayInput() *overlayInput {
	o := &overlayInput{}
	o.cond = sync.NewCond(&o.mu)
	return o
}

func (o *overlayInput) Read(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for (o.stopped || len(o.buf) == 0) && o.err == nil {
		o.cond.Wait()
	}
	if o.stopped || len(o.buf) == 0 {
		return 0, o.err
	}
	n := copy(p, o.buf)
	o.buf = o.buf[n:]
	return n, nil
}

func (o *overlayInput) write(p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(p) == 0 || o.err != nil || len(o.buf)+len(p) > overlayInputMax {
		return
	}
	o.buf = append(o.buf, p...)
	o.cond.Broadcast()
}

// stop hands out no more input. Bubble Tea keeps reading until it has shut
// down and drops what it reads then; input waiting here instead goes to
// the session on release. A read in progress waits for detach.
func (o *overlayInput) stop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stopped = true
}

// end makes reads return err once the buffer is drained.
func (o *overlayInput) end(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err == nil {
		o.err = err
	}
	o.cond.Broadcast()
}

// detach fails further reads, and any waiting one, and returns what was
// not read.
func (o *overlayInput) detach() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	unread := o.buf
	o.buf, o.stopped = nil, true
	if o.err == nil {
		o.err = io.ErrClosedPipe
	}
	o.cond.Broadcast()
	return unread
}

// queueWriter writes to w from its own goroutine. Write waits while max
// bytes are queued, unless told not to; push never waits.
type queueWriter struct {
	w      io.Writer
	max    int
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	err    error
	noWait bool
	shut   bool
	done   chan struct{}
}

func newQueueWriter(w io.Writer, max int) *queueWriter {
	q := &queueWriter{w: w, max: max, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

// Write queues p, first waiting for room unless noWait is set. It fails
// once a write to w failed or the queue is closed.
func (q *queueWriter) Write(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.buf) >= q.max && !q.noWait && q.err == nil && !q.shut {
		q.cond.Wait()
	}
	if err := q.queue(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// push queues p without waiting.
func (q *queueWriter) push(p []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queue(p)
}

func (q *queueWriter) queue(p []byte) error {
	switch {
	case q.err != nil:
		return q.err
	case q.shut:
		return io.ErrClosedPipe
	}
	if len(p) > 0 {
		q.buf = append(q.buf, p...)
		q.cond.Broadcast()
	}
	return nil
}

// setNoWait makes Write stop waiting for room, or wait again.
func (q *queueWriter) setNoWait(on bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.noWait = on
	q.cond.Broadcast()
}

func (q *queueWriter) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		for len(q.buf) == 0 && !q.shut {
			q.cond.Wait()
		}
		if len(q.buf) == 0 {
			q.mu.Unlock()
			return
		}
		p := q.buf
		q.buf = nil
		q.cond.Broadcast()
		q.mu.Unlock()
		if _, err := q.w.Write(p); err != nil {
			q.mu.Lock()
			q.err = err
			q.cond.Broadcast()
			q.mu.Unlock()
			return
		}
	}
}

// Close writes out what is queued and stops. It returns the first write
// error. Later writes fail.
func (q *queueWriter) Close() error {
	q.mu.Lock()
	q.shut = true
	q.cond.Broadcast()
	q.mu.Unlock()
	<-q.done
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}
