//go:build windows

package container

import "os"

// notifyResize is a no-op on Windows. SIGWINCH does not exist on Windows;
// terminal resize events are delivered through the console API instead.
func notifyResize(_ chan<- os.Signal) {}
