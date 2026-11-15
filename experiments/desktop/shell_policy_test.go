package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNotificationEligibility(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name                string
		focused             bool
		notifyWhenFocused   bool
		connection, current string
		deadline            time.Time
		want                bool
	}{
		{name: "background current session", connection: "one", current: "one", want: true},
		{name: "focused with override", focused: true, notifyWhenFocused: true, connection: "one", current: "one", want: true},
		{name: "override still rejects expired request", focused: true, notifyWhenFocused: true, connection: "one", current: "one", deadline: now},
		{name: "override still rejects stale connection", focused: true, notifyWhenFocused: true, connection: "one", current: "two"},
		{name: "focused completion stays quiet", focused: true, connection: "one", current: "one"},
		{name: "focused tool approval stays quiet", focused: true, connection: "one", current: "one"},
		{name: "focused network approval stays quiet", focused: true, connection: "one", current: "one", deadline: now.Add(time.Second)},
		{name: "reconnected window", focused: true, connection: "one", current: "two"},
		{name: "disconnected window", connection: "one"},
		{name: "expired request", focused: true, connection: "one", current: "one", deadline: now},
		{name: "active request", connection: "one", current: "one", deadline: now.Add(time.Second), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, notificationEligible(tc.focused, tc.notifyWhenFocused, tc.connection, tc.current, tc.deadline, now))
		})
	}
}
