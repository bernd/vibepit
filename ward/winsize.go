package ward

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// winsizeRewriter filters XTWINOPS window-size reports on their way from
// the real terminal to the child PTY.
//
// Ward shrinks the child's PTY by one row to reserve space for its bar, but
// applications such as tmux ask the terminal directly for its size
// (CSI 18 t / CSI 14 t) and trust the reply over TIOCGWINSZ. The real
// terminal answers with the full height, so tmux would grow back onto the
// protected row. Rewriting the reports keeps the child's view of the
// terminal consistent with the PTY size ward hands it.
//
// Rewritten sequences:
//   - CSI 8 ; rows ; cols t   (cells)  -> rows replaced by rows-1
//   - CSI 9 ; rows ; cols t   (screen)  -> rows replaced by rows-1
//   - CSI 4 ; height ; width t (pixels) -> height scaled by (rows-1)/rows
//   - CSI 48 ; rows ; cols ; height ; width t (in-band resize, mode 2048)
//     -> rows and pixel height adjusted as above
//
// Only this report family is covered. A child could still probe the real
// height with a cursor position report (CSI 6 n) after moving the cursor
// past the scroll region; no known application does, so that is out of
// scope.
type winsizeRewriter struct {
	w    io.Writer
	rows *atomic.Int32 // full terminal height, as seen by ward

	mu      sync.Mutex
	pending [maxPending]byte // partial CSI that may still become a size report
	npend   int
	flush   *time.Timer // releases pending bytes if no continuation arrives
	gen     uint64      // bumped whenever pending changes; stale flushes bail out
}

// winsizeFlushDelay bounds how long a size-report prefix is held back.
// Terminal reports arrive in one burst, so a prefix that is not completed
// quickly is user input (e.g. Esc followed by '[').
const winsizeFlushDelay = 50 * time.Millisecond

// maxPending caps how much of an unfinished sequence is held back. A size
// report is at most ESC [ 48 ; n ; n ; n ; n t with five-digit values;
// anything longer is passed through verbatim.
const maxPending = 32

func newWinsizeRewriter(w io.Writer, rows *atomic.Int32) *winsizeRewriter {
	return &winsizeRewriter{w: w, rows: rows}
}

// Write implements io.Writer. Bytes that might be the prefix of a size
// report are held back until the sequence completes, is ruled out, or the
// flush delay expires; they count as consumed by the call that held them.
// A lone trailing ESC is never held back: terminals emit reports
// atomically, so a split right after ESC is far less likely than a user
// pressing Esc. On error, n is the number of bytes of p handed to the
// underlying writer and any held-back bytes are discarded.
func (r *winsizeRewriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.npend == 0 && bytes.IndexByte(p, asciiESC) < 0 {
		_, err := r.w.Write(p)
		return len(p), err
	}

	r.gen++
	if r.flush != nil {
		r.flush.Stop()
	}

	data := p
	if r.npend > 0 {
		data = append(r.pending[:r.npend:r.npend], p...)
		r.npend = 0
	}
	// Bytes of p still unwritten; the held prefix belonged to an earlier call.
	consumed := func() int { return max(0, len(p)-len(data)) }

	for len(data) > 0 {
		idx := bytes.IndexByte(data, asciiESC)
		if idx < 0 {
			idx = len(data)
		}
		if idx > 0 {
			if _, err := r.w.Write(data[:idx]); err != nil {
				return consumed(), err
			}
			data = data[idx:]
			continue
		}

		n, repl, incomplete := r.matchSizeReport(data)
		switch {
		case incomplete && len(data) > 1 && len(data) <= maxPending:
			r.npend = copy(r.pending[:], data)
			data = nil
		case n > 0:
			if _, err := r.w.Write(repl); err != nil {
				return consumed(), err
			}
			data = data[n:]
		default:
			// Not a size report: forward everything up to the next ESC in
			// one write, so the child never sees a sequence split after
			// its ESC and mistakes it for a bare Esc key.
			end := bytes.IndexByte(data[1:], asciiESC)
			if end < 0 {
				end = len(data)
			} else {
				end++
			}
			if _, err := r.w.Write(data[:end]); err != nil {
				return consumed(), err
			}
			data = data[end:]
		}
	}

	if r.npend > 0 {
		gen := r.gen
		if r.flush == nil {
			r.flush = time.AfterFunc(winsizeFlushDelay, func() { r.flushPending(gen) })
		} else {
			// Reset does not wait for a running callback; the generation
			// check in flushPending makes such a stale run a no-op.
			r.flush.Reset(winsizeFlushDelay)
		}
	}
	return len(p), nil
}

// flushPending writes held-back bytes verbatim once the flush delay expires.
// gen identifies the Write that armed the timer; a later Write supersedes it.
func (r *winsizeRewriter) flushPending(gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.npend == 0 || gen != r.gen {
		return
	}
	n := r.npend
	r.npend = 0
	r.w.Write(r.pending[:n]) //nolint:errcheck
}

// maxReportParams is the longest parameter list of any size report
// (CSI 48 ; rows ; cols ; height ; width t).
const maxReportParams = 5

// matchSizeReport inspects data, which must start with ESC. It returns the
// number of bytes consumed and their replacement when a complete size
// report is present, incomplete=true when data is a proper prefix of one,
// and (0, nil, false) when data cannot be a size report.
//
// Size reports are plain CSI sequences with numeric parameters only, so
// the match is a direct scan rather than a full ANSI parse. Anything else
// (private prefixes, intermediates, OSC/DCS strings) is rejected as soon
// as it is recognisable, so unrelated input is never delayed.
func (r *winsizeRewriter) matchSizeReport(data []byte) (int, []byte, bool) {
	var ps [maxReportParams]int
	n, nps, incomplete := scanSizeReport(data, &ps)
	if n == 0 {
		return 0, nil, incomplete
	}
	return r.rewriteReport(n, ps[:nps])
}

// looksLikeSizeReport reports whether data, starting with ESC, is a
// complete size report or an unambiguous prefix of one (at least ESC [).
func looksLikeSizeReport(data []byte) bool {
	var ps [maxReportParams]int
	n, _, incomplete := scanSizeReport(data, &ps)
	return n > 0 || (incomplete && len(data) >= 2)
}

// scanSizeReport parses ESC [ digits ( ; digits )* t at the start of data
// into ps. It returns the sequence length and parameter count on a full
// match, incomplete=true for a proper prefix, and n=0, incomplete=false
// when data cannot be a size report.
func scanSizeReport(data []byte, ps *[maxReportParams]int) (int, int, bool) {
	if len(data) < 2 {
		return 0, 0, true
	}
	if data[1] != '[' {
		return 0, 0, false
	}

	nps := 0
	val, ndigits := 0, 0
	for i := 2; i < len(data); i++ {
		c := data[i]
		switch {
		case c >= '0' && c <= '9':
			val = val*10 + int(c-'0')
			if val > parser.MaxParam {
				return 0, 0, false
			}
			ndigits++
		case c == ';' || c == 't':
			// Missing values never occur in a size report.
			if ndigits == 0 || nps == maxReportParams {
				return 0, 0, false
			}
			ps[nps] = val
			nps++
			val, ndigits = 0, 0
			if c == 't' {
				return i + 1, nps, false
			}
		default:
			return 0, 0, false
		}
	}
	return 0, 0, true
}

// rewriteReport builds the replacement for a complete size report of n
// bytes with parameters ps, or reports (0, nil, false) if it is not a
// kind ward needs to adjust.
func (r *winsizeRewriter) rewriteReport(n int, ps []int) (int, []byte, bool) {
	// ps[1] is the row count (or pixel height for kind 4);
	// ps[3] is the pixel height for in-band resize reports.
	kind := ps[0]
	switch {
	case (kind == 8 || kind == 9) && len(ps) == 3:
		ps[1] = shrinkRows(ps[1])
	case kind == 4 && len(ps) == 3:
		// The pixel reply carries no row count, so scale by ward's view of
		// the terminal height.
		rows := int(r.rows.Load())
		if rows <= 1 {
			return 0, nil, false
		}
		ps[1] = ps[1] * (rows - 1) / rows
	case kind == 48 && len(ps) == 5:
		// Scale by the report's own row count rather than ward's, which may
		// lag behind during a resize; the pair must stay self-consistent so
		// the child derives the right cell height.
		rows := ps[1]
		if rows <= 1 {
			return 0, nil, false
		}
		ps[1] = rows - 1
		ps[3] = ps[3] * (rows - 1) / rows
	default:
		return 0, nil, false
	}
	return n, []byte(ansi.WindowOp(kind, ps[1:]...)), false
}

func shrinkRows(v int) int {
	if v > 1 {
		return v - 1
	}
	return v
}
