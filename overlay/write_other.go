//go:build !unix

package overlay

import "io"

// newBlockingWriter returns w: only Unix files turn non-blocking under a
// running program.
func newBlockingWriter(w io.Writer) io.Writer { return w }
