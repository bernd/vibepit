package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpdateFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			"use with images is error",
			[]string{"vibepit", "update", "--use", "0.1.0", "--images"},
			"--use cannot be combined with --images",
		},
		{
			"list with check is error",
			[]string{"vibepit", "update", "--list", "--check"},
			"--list and --check are mutually exclusive",
		},
		{
			"list with use is error",
			[]string{"vibepit", "update", "--list", "--use", "0.1.0"},
			"--list and --use are mutually exclusive",
		},
		{
			"check with use is error",
			[]string{"vibepit", "update", "--check", "--use", "0.1.0"},
			"--check and --use are mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := RootCommand()
			err := app.Run(context.Background(), tt.args)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
