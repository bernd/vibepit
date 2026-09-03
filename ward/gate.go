package ward

import (
	"io"
	"sync"
	"time"
)

// gateHoldTimeout bounds how long a write of ward's own waits for the child
// stream to reach a safe point. A child that stops inside a sequence or a
// character (an OSC with no terminator, a truncated write) would otherwise
// hide the bar until it prints again. Matches the hold on the input path.
const gateHoldTimeout = 50 * time.Millisecond

// outputGate merges ward's own escape sequences into the child's output
// stream only at safe points: never inside an escape sequence and never
// between the bytes of one UTF-8 character. A PTY read can end anywhere,
// and the event loop and resize handler write the bar between reads, so
// a bar written blindly can land inside a character and turn it into two
// replacement glyphs on the terminal.
//
// A held sequence goes out at the first safe byte of the next chunk, not
// at its end, so it lands where the caller meant it to within a few bytes.
// If no safe point arrives within holdTimeout, the held bytes are written
// anyway: the terminal restarts parsing on the ESC they begin with, and
// the gate does the same.
//
// Callers hold mu around every method; the hold timer locks it itself.
type outputGate struct {
	w           io.Writer
	mu          *sync.Mutex
	holdTimeout time.Duration
	scanner     escScanner
	deferred    []byte
	holdTimer   *time.Timer
}

func newOutputGate(w io.Writer, mu *sync.Mutex) *outputGate {
	return &outputGate{w: w, mu: mu, holdTimeout: gateHoldTimeout}
}

// Forward writes a chunk of child output. Bytes held by earlier Emits go
// out at the first safe point inside the chunk.
func (g *outputGate) Forward(data []byte) scanResult {
	var r scanResult
	if len(g.deferred) > 0 {
		for i := range data {
			r.merge(g.scanner.Scan(data[i : i+1]))
			if g.scanner.InGround() {
				g.w.Write(data[:i+1]) //nolint:errcheck
				g.flush()
				data = data[i+1:]
				break
			}
		}
		if len(g.deferred) > 0 {
			// No safe point in this chunk; everything was scanned.
			g.w.Write(data) //nolint:errcheck
			return r
		}
	}
	r.merge(g.scanner.Scan(data))
	g.w.Write(data) //nolint:errcheck
	return r
}

// Emit writes seq now when the stream is at a safe point, otherwise holds
// it until the next Forward reaches one or holdTimeout passes. Order among
// held sequences and relative to later Emits is preserved.
func (g *outputGate) Emit(seq string) {
	if g.Ready() {
		io.WriteString(g.w, seq) //nolint:errcheck
		return
	}
	g.deferred = append(g.deferred, seq...)
	if g.holdTimer == nil {
		g.holdTimer = time.AfterFunc(g.holdTimeout, g.forceFlush)
	}
}

// Ready reports whether the child stream is at a safe injection point.
func (g *outputGate) Ready() bool {
	return g.scanner.InGround()
}

// Drain writes held bytes regardless of stream state. For use once the
// child has exited and nothing more will arrive.
func (g *outputGate) Drain() {
	g.writeDeferred()
}

func (g *outputGate) flush() {
	if len(g.deferred) == 0 || !g.Ready() {
		return
	}
	g.writeDeferred()
}

func (g *outputGate) forceFlush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.deferred) == 0 {
		return
	}
	g.scanner = escScanner{}
	g.writeDeferred()
}

func (g *outputGate) writeDeferred() {
	if g.holdTimer != nil {
		g.holdTimer.Stop()
		g.holdTimer = nil
	}
	if len(g.deferred) == 0 {
		return
	}
	g.w.Write(g.deferred) //nolint:errcheck
	g.deferred = g.deferred[:0]
}

func (r *scanResult) merge(o scanResult) {
	r.ScrollReset = r.ScrollReset || o.ScrollReset
	r.BarErased = r.BarErased || o.BarErased
}
