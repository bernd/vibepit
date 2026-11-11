//go:build windows

package container

import "os"

// NotifyResize is a no-op on Windows. SIGWINCH does not exist on Windows;
// terminal resize events are delivered through the console API instead.
func NotifyResize(_ chan<- os.Signal) {}
