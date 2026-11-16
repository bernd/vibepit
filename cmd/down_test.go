package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/adrg/xdg"
	ctr "github.com/bernd/vibepit/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessionStopper struct {
	sessions    map[string][]ctr.SessionContainer
	order       []string
	failRemove  map[string]bool
	failNetwork map[string]bool
	removed     []string
	networks    []string // removals attempted
}

func (f *fakeSessionStopper) ProjectSessionIDs(context.Context, string) ([]string, error) {
	return f.order, nil
}

func (f *fakeSessionStopper) SessionContainers(_ context.Context, sessionID string) ([]ctr.SessionContainer, error) {
	return f.sessions[sessionID], nil
}

func (f *fakeSessionStopper) StopAndRemove(_ context.Context, containerID string) error {
	if f.failRemove[containerID] {
		return errors.New("boom")
	}
	f.removed = append(f.removed, containerID)
	return nil
}

func (f *fakeSessionStopper) RemoveNetwork(_ context.Context, networkID string) error {
	f.networks = append(f.networks, networkID)
	if f.failNetwork[networkID] {
		return errors.New("boom")
	}
	return nil
}

func TestStopProjectSessions(t *testing.T) {
	networks := func(ids ...string) []string {
		var names []string
		for _, id := range ids {
			names = append(names, networkNamePrefix+id)
		}
		return names
	}
	tests := []struct {
		name         string
		failRemove   map[string]bool
		failNetwork  map[string]bool
		wantErr      string
		wantRemoved  []string
		wantNetworks []string
		wantCredsFor []string // sessions whose credentials must be kept
	}{
		{
			name:         "removes every session",
			wantRemoved:  []string{"sandbox1", "proxy1", "proxy2", "proxy3"},
			wantNetworks: networks("s1", "s2", "s3"),
		},
		{
			name:         "skips the network and keeps credentials when a container is left",
			failRemove:   map[string]bool{"proxy2": true},
			wantErr:      "could not fully remove session s2",
			wantRemoved:  []string{"sandbox1", "proxy1", "proxy3"},
			wantNetworks: networks("s1", "s3"),
			wantCredsFor: []string{"s2"},
		},
		{
			name:         "removes credentials but fails when the network is left",
			failNetwork:  map[string]bool{networkNamePrefix + "s2": true},
			wantErr:      "could not fully remove session s2",
			wantRemoved:  []string{"sandbox1", "proxy1", "proxy2", "proxy3"},
			wantNetworks: networks("s1", "s2", "s3"),
		},
		{
			name:         "names every failed session",
			failRemove:   map[string]bool{"sandbox1": true},
			failNetwork:  map[string]bool{networkNamePrefix + "s3": true},
			wantErr:      "could not fully remove sessions s1, s3",
			wantRemoved:  []string{"proxy1", "proxy2", "proxy3"},
			wantNetworks: networks("s2", "s3"),
			wantCredsFor: []string{"s1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origStateHome, origRuntimeDir := xdg.StateHome, xdg.RuntimeDir
			xdg.StateHome, xdg.RuntimeDir = t.TempDir(), t.TempDir()
			t.Cleanup(func() { xdg.StateHome, xdg.RuntimeDir = origStateHome, origRuntimeDir })

			order := []string{"s1", "s2", "s3"}
			for _, id := range order {
				require.NoError(t, os.MkdirAll(filepath.Join(sessionBaseDir(), id), 0o700))
			}

			client := &fakeSessionStopper{
				order: order,
				sessions: map[string][]ctr.SessionContainer{
					"s1": {{ID: "sandbox1", Role: ctr.RoleSandbox}, {ID: "proxy1", Role: ctr.RoleProxy}},
					"s2": {{ID: "proxy2", Role: ctr.RoleProxy}},
					"s3": {{ID: "proxy3", Role: ctr.RoleProxy}},
				},
				failRemove:  tt.failRemove,
				failNetwork: tt.failNetwork,
			}

			err := stopProjectSessions(context.Background(), client, "/project")
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantRemoved, client.removed)
			assert.Equal(t, tt.wantNetworks, client.networks)
			for _, id := range order {
				_, statErr := os.Stat(filepath.Join(sessionBaseDir(), id))
				if slices.Contains(tt.wantCredsFor, id) {
					assert.NoError(t, statErr, "credentials of %s must be kept", id)
				} else {
					assert.True(t, os.IsNotExist(statErr), "credentials of %s must be removed", id)
				}
			}
		})
	}
}

func TestStopProjectSessionsWithoutSessions(t *testing.T) {
	err := stopProjectSessions(context.Background(), &fakeSessionStopper{}, "/project")
	assert.EqualError(t, err, "no session found for /project")
}
