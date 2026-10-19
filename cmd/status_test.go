package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatMemoryLimit(t *testing.T) {
	tests := []struct {
		limit int64
		want  string
	}{
		{limit: 0, want: "unlimited"},
		{limit: 512 << 20, want: "512MiB"},
		{limit: 8 << 30, want: "8GiB"},
		{limit: 3 << 29, want: "1.5GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatMemoryLimit(tt.limit))
		})
	}
}
