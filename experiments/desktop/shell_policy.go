package main

import "time"

// A notice belongs to one connection generation, even if the window is reused.
func notificationEligible(focused, notifyWhenFocused bool, connection, current string, deadline, now time.Time) bool {
	return (!focused || notifyWhenFocused) && connection == current && (deadline.IsZero() || deadline.After(now))
}
