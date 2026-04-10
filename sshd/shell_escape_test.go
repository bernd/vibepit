package sshd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Empty string.
		{name: "empty", input: "", want: "''"},

		// Safe characters returned unchanged.
		{name: "lowercase", input: "hello", want: "hello"},
		{name: "uppercase", input: "HELLO", want: "HELLO"},
		{name: "digits", input: "12345", want: "12345"},
		{name: "mixed alphanumeric", input: "abcXYZ123", want: "abcXYZ123"},
		{name: "hyphen", input: "a-b", want: "a-b"},
		{name: "underscore", input: "a_b", want: "a_b"},
		{name: "dot", input: "file.txt", want: "file.txt"},
		{name: "slash", input: "/usr/bin/bash", want: "/usr/bin/bash"},
		{name: "colon", input: "host:port", want: "host:port"},
		{name: "comma", input: "a,b", want: "a,b"},
		{name: "plus", input: "a+b", want: "a+b"},
		{name: "equals", input: "key=value", want: "key=value"},
		{name: "all safe chars", input: "a-b_c.d/e:f,g+h=i", want: "a-b_c.d/e:f,g+h=i"},

		// Strings requiring quoting.
		{name: "space", input: "hello world", want: "'hello world'"},
		{name: "tab", input: "a\tb", want: "'a\tb'"},
		{name: "newline", input: "a\nb", want: "'a\nb'"},
		{name: "semicolon", input: "a;b", want: "'a;b'"},
		{name: "ampersand", input: "a&b", want: "'a&b'"},
		{name: "pipe", input: "a|b", want: "'a|b'"},
		{name: "dollar", input: "$HOME", want: "'$HOME'"},
		{name: "backtick", input: "`cmd`", want: "'`cmd`'"},
		{name: "double quote", input: `say "hi"`, want: `'say "hi"'`},
		{name: "backslash", input: `a\b`, want: `'a\b'`},
		{name: "parentheses", input: "a(b)", want: "'a(b)'"},
		{name: "angle brackets", input: "a>b<c", want: "'a>b<c'"},
		{name: "exclamation", input: "hello!", want: "'hello!'"},
		{name: "asterisk", input: "*.go", want: "'*.go'"},
		{name: "question mark", input: "file?.txt", want: "'file?.txt'"},
		{name: "hash", input: "#comment", want: "'#comment'"},
		{name: "tilde", input: "~user", want: "'~user'"},
		{name: "at sign", input: "user@host", want: "'user@host'"},

		// Single quote escaping.
		{name: "single quote", input: "it's", want: "'it'\\''s'"},
		{name: "only single quote", input: "'", want: "''\\'''"},
		{name: "multiple single quotes", input: "it's a 'test'", want: "'it'\\''s a '\\''test'\\'''"},
		{name: "leading single quote", input: "'hello", want: "''\\''hello'"},
		{name: "trailing single quote", input: "hello'", want: "'hello'\\'''"},

		// Non-printable and control characters.
		{name: "null byte", input: "a\x00b", want: "'a\x00b'"},
		{name: "escape char", input: "a\x1bb", want: "'a\x1bb'"},
		{name: "bell", input: "\a", want: "'\a'"},
		{name: "carriage return", input: "a\rb", want: "'a\rb'"},

		// Unicode and multibyte characters.
		{name: "unicode letter", input: "café", want: "'café'"},
		{name: "emoji", input: "hello 👋", want: "'hello 👋'"},
		{name: "cjk", input: "日本語", want: "'日本語'"},
		{name: "unicode only", input: "über", want: "'über'"},

		// Whitespace-only strings.
		{name: "single space", input: " ", want: "' '"},
		{name: "multiple spaces", input: "   ", want: "'   '"},
		{name: "only tab", input: "\t", want: "'\t'"},
		{name: "only newline", input: "\n", want: "'\n'"},

		// Combined metacharacter sequences.
		{name: "shell injection", input: ";${}", want: "';${}'"},
		{name: "subshell", input: "$(rm -rf /)", want: "'$(rm -rf /)'"},
		{name: "command chain", input: "a && b || c", want: "'a && b || c'"},
		{name: "redirect and pipe", input: "cat < in > out | tee log", want: "'cat < in > out | tee log'"},
		{name: "brace expansion", input: "{a,b,c}", want: "'{a,b,c}'"},
		{name: "bracket glob", input: "[abc]", want: "'[abc]'"},

		// Single quotes mixed with other special characters.
		{name: "single quote and space", input: "' ", want: "''\\'' '"},
		{name: "single quote and dollar", input: "'$HOME'", want: "''\\''$HOME'\\'''"},
		{name: "single quote and semicolon", input: "it';s", want: "'it'\\'';s'"},
		{name: "double and single quotes", input: `"it's"`, want: "'\"it'\\''s\"'"},
		{name: "consecutive single quotes", input: "''", want: "''\\'''\\'''"},

		// Single safe character boundaries.
		{name: "single letter", input: "a", want: "a"},
		{name: "single digit", input: "0", want: "0"},
		{name: "single hyphen", input: "-", want: "-"},
		{name: "single unsafe char", input: " ", want: "' '"},

		// Realistic inputs.
		{name: "ssh command", input: "ssh user@host -p 22", want: "'ssh user@host -p 22'"},
		{name: "env var assignment", input: "FOO=bar baz", want: "'FOO=bar baz'"},
		{name: "path with spaces", input: "/home/user/my docs/file.txt", want: "'/home/user/my docs/file.txt'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShellEscape(tt.input))
		})
	}
}
