package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/tui"
)

// confirmProjectConfig asks the user before vibepit loads a project config
// at a root it hasn't recorded. The sandbox can write a project config, and
// a .git beside it, in any subdirectory of a project; a later session
// started from there would load that config without anyone having chosen
// it. It returns the contents the user confirmed, for loading in place of
// the file, or nil when there is no config: first-run setup writes it.
func confirmProjectConfig(projectPath string, in io.Reader, out io.Writer, interactive bool) ([]byte, error) {
	data, err := os.ReadFile(projectPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}
	if !interactive {
		return nil, fmt.Errorf("vibepit didn't create the project config %s; run vibepit in a terminal to review it", projectPath)
	}

	label := lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)
	prompt := lipgloss.NewStyle().Foreground(tui.ColorOrange).Bold(true)
	fmt.Fprintf(out, "%s\n\n%s\n\n",
		label.Render("Vibepit didn't create this project config, so it may come from code in a sandbox:"),
		projectPath)
	fmt.Fprintln(out, strings.TrimRight(tui.EscapeText(string(data)), "\n"))
	fmt.Fprint(out, prompt.Render("\nUse this project config?")+" [y/N] ")

	// Fscanln reads stdin a byte at a time and leaves the rest for the
	// session, unlike a buffered reader.
	var answer string
	fmt.Fscanln(in, &answer) //nolint:errcheck // an empty answer means no
	answer = strings.ToLower(answer)
	if answer != "y" && answer != "yes" {
		return nil, fmt.Errorf("not using the project config %s; review or remove it, then run vibepit again", projectPath)
	}
	return data, nil
}
