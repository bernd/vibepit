package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolAllowlist(t *testing.T) {
	al, err := NewMCPToolAllowlist([]string{
		"get_*",
		"search_in_files_by_text",
		"find_*",
		"list_directory_tree",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		tool string
		want bool
	}{
		{"exact match", "search_in_files_by_text", true},
		{"exact match 2", "list_directory_tree", true},
		{"glob prefix", "get_file_text_by_path", true},
		{"glob prefix 2", "get_symbol_info", true},
		{"glob find", "find_files_by_glob", true},
		{"not allowed", "execute_terminal_command", false},
		{"not allowed 2", "replace_text_in_file", false},
		{"not allowed 3", "rename_refactoring", false},
		{"empty tool", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.tool))
		})
	}
}

func TestMCPToolAllowlistEmpty(t *testing.T) {
	al, err := NewMCPToolAllowlist(nil)
	require.NoError(t, err)
	assert.False(t, al.Allows("anything"))
}

func TestMCPToolAllowlistValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{"valid exact", []string{"get_file"}, false},
		{"valid glob", []string{"get_*"}, false},
		{"empty entry", []string{""}, true},
		{"spaces", []string{"get file"}, true},
		{"bare star allowed", []string{"*"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewMCPToolAllowlist(tt.entries)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMCPToolAllowlistAllowAll(t *testing.T) {
	al, err := NewMCPToolAllowlist([]string{"*"})
	require.NoError(t, err)
	assert.True(t, al.Allows("anything"))
	assert.True(t, al.Allows("execute_terminal_command"))
	assert.False(t, al.Allows(""))
}
