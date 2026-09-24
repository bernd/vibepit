package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ascii unchanged", in: "api.example.com:443", want: "api.example.com:443"},
		{name: "unicode unchanged", in: "bücher.example", want: "bücher.example"},
		{name: "empty", in: "", want: ""},
		{name: "tab kept", in: "a\tb", want: "a\tb"},
		{name: "escape sequence loses ESC", in: "evil\x1b[2J.com", want: "evil[2J.com"},
		{name: "newline and carriage return dropped", in: "a\r\nb", want: "ab"},
		{name: "other C0 dropped", in: "a\x00\x07\x08b", want: "ab"},
		{name: "DEL dropped", in: "a\x7fb", want: "ab"},
		{name: "C1 CSI dropped", in: "a\u009b31mb", want: "a31mb"},
		{name: "C1 range bounds dropped", in: "\u0080a\u009f", want: "a"},
		{name: "first rune after C1 kept", in: " ", want: " "},
		{name: "invalid utf-8 dropped", in: "a\xffb", want: "ab"},
		{name: "raw C1 byte is invalid utf-8 and dropped", in: "a\x9bb", want: "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeText(tt.in))
		})
	}
}
