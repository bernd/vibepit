package sshd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatCoarseDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "<1m"},
		{"sub-minute", 30 * time.Second, "<1m"},
		{"exactly 1 minute", time.Minute, "1m"},
		{"minutes only", 7 * time.Minute, "7m"},
		{"minutes rounds down", 7*time.Minute + 45*time.Second, "7m"},
		{"1 hour exactly", time.Hour, "1h"},
		{"hours and minutes", 2*time.Hour + 15*time.Minute, "2h15m"},
		{"hours with zero minutes", 3 * time.Hour, "3h"},
		{"1 day exactly", 24 * time.Hour, "1d"},
		{"days and hours", 26 * time.Hour, "1d2h"},
		{"days with zero hours", 48 * time.Hour, "2d"},
		{"days hours minutes", 25*time.Hour + 30*time.Minute, "1d1h"},
		{"large duration", 100*24*time.Hour + 5*time.Hour, "100d5h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatCoarseDuration(tc.d))
		})
	}
}
