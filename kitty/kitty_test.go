package kitty

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOverlayArgs(t *testing.T) {
	args := overlayArgs("unix:/tmp/kitty-1", "42", "t1", []string{"/usr/bin/vibepit", "approve", "--session", "abc", "example.com:443"})
	assert.Equal(t, []string{
		"@", "--to", "unix:/tmp/kitty-1",
		"launch", "--type=overlay", "--match", "window_id:42", "--next-to", "id:42",
		"--var", "vibepit_overlay=t1", "--wait-for-child-to-exit",
		"--", "/usr/bin/vibepit", "approve", "--session", "abc", "example.com:443",
	}, args)
}

func TestCloseArgs(t *testing.T) {
	assert.Equal(t, []string{
		"@", "--to", "unix:/tmp/kitty-1",
		"close-window", "--match", "var:vibepit_overlay=t1",
	}, closeArgs("unix:/tmp/kitty-1", "t1"))
}

func TestDetect(t *testing.T) {
	haveKitten := func(string) (string, error) { return "/usr/bin/kitten", nil }
	noKitten := func(string) (string, error) { return "", errors.New("not found") }

	tests := []struct {
		name     string
		env      map[string]string
		lookPath func(string) (string, error)
		wantErr  error
		wantSock string
		wantWin  string
	}{
		{
			name:     "kitty with socket",
			env:      map[string]string{"KITTY_LISTEN_ON": "unix:/tmp/k", "KITTY_WINDOW_ID": "7"},
			lookPath: haveKitten,
			wantSock: "unix:/tmp/k",
			wantWin:  "7",
		},
		{
			name:     "abstract unix socket passes through verbatim",
			env:      map[string]string{"KITTY_LISTEN_ON": "unix:@mykitty-427848", "KITTY_WINDOW_ID": "3"},
			lookPath: haveKitten,
			wantSock: "unix:@mykitty-427848",
			wantWin:  "3",
		},
		{
			name:     "kitty without socket",
			env:      map[string]string{"KITTY_WINDOW_ID": "7"},
			lookPath: haveKitten,
			wantErr:  ErrNotKitty,
		},
		{
			name:     "not kitty",
			env:      map[string]string{},
			lookPath: haveKitten,
			wantErr:  ErrNotKitty,
		},
		{
			name:     "kitten missing",
			env:      map[string]string{"KITTY_LISTEN_ON": "unix:/tmp/k", "KITTY_WINDOW_ID": "7"},
			lookPath: noKitten,
			wantErr:  ErrNoKitten,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(k string) string { return tt.env[k] }
			e, err := Detect(lookup, tt.lookPath)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantSock, e.ListenOn)
			assert.Equal(t, tt.wantWin, e.WindowID)
		})
	}
}
