//go:build !windows

package container

import (
	"os"
	"os/signal"
	"syscall"
)

// NotifyResize relays the local terminal's resize signals to ch.
func NotifyResize(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}
