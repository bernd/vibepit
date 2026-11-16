package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const pathsProjectFlag = "project"

type pathEntry struct {
	name  string // argument that selects this path
	label string
	path  func() (string, error)
}

func PathsCommand() *cli.Command {
	return &cli.Command{
		Name:      "paths",
		Usage:     "Show where vibepit keeps its files",
		ArgsUsage: "[global-config|project-config|sessions|cache]",
		Description: "Without an argument, lists every file and directory vibepit uses for the\n" +
			"project in the current directory, or in --project. With a name, prints\n" +
			"only that path, for use in scripts, e.g.\n" +
			"$EDITOR \"$(vibepit paths global-config)\".",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  pathsProjectFlag,
				Usage: "Project directory (default: current directory)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() > 1 {
				return fmt.Errorf("expected at most one path name, got %d", cmd.Args().Len())
			}
			projectRoot := func() (string, error) {
				dir := cmd.String(pathsProjectFlag)
				if dir == "" {
					wd, err := os.Getwd()
					if err != nil {
						return "", err
					}
					dir = wd
				}
				return config.FindProjectRoot(dir)
			}
			return printPaths(cmd.Root().Writer, pathEntries(projectRoot), cmd.Args().First())
		},
	}
}

// pathEntries takes the project root as a function so that printing a path
// which doesn't depend on it skips the git call that finds it.
func pathEntries(projectRoot func() (string, error)) []pathEntry {
	fixed := func(p string) func() (string, error) {
		return func() (string, error) { return p, nil }
	}
	return []pathEntry{
		{"global-config", "Global config", fixed(config.DefaultGlobalPath())},
		{"project-config", "Project config", func() (string, error) {
			root, err := projectRoot()
			if err != nil {
				return "", err
			}
			return config.DefaultProjectPath(root), nil
		}},
		{"sessions", "Sessions", fixed(sessionBaseDir())},
		{"cache", "Cache", fixed(config.CacheDir())},
	}
}

// printPaths lists all entries, or prints the bare path of the one called
// name so scripts can use the output directly.
func printPaths(w io.Writer, entries []pathEntry, name string) error {
	if name != "" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.name == name {
				p, err := e.path()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(w, p)
				return err
			}
			names = append(names, e.name)
		}
		return fmt.Errorf("unknown path %q, expected one of: %s", name, strings.Join(names, ", "))
	}

	width := 0
	for _, e := range entries {
		width = max(width, len(e.label))
	}
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorField)
	noteStyle := lipgloss.NewStyle().Foreground(tui.ColorRejected)
	// Resolve every path before printing so an error doesn't leave a
	// partial listing behind.
	paths := make([]string, len(entries))
	for i, e := range entries {
		p, err := e.path()
		if err != nil {
			return err
		}
		paths[i] = p
	}
	for i, e := range entries {
		p := paths[i]
		line := labelStyle.Render(fmt.Sprintf("%-*s", width, e.label)) + "  " + p
		// A missing file is normal (vibepit creates them on demand), but
		// worth showing when someone wonders why their config is ignored.
		// So is a path vibepit can't read, which makes commands that load
		// it fail.
		if note := statNote(p); note != "" {
			line += " " + noteStyle.Render("("+note+")")
		}
		if _, err := lipgloss.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// statNote describes why path can't be used, or returns "" if it exists and
// can be read.
func statNote(path string) string {
	info, err := os.Stat(path)
	// Stat succeeds on a file whose mode denies reading, so open regular
	// files and directories as well. Other types are skipped because opening
	// a FIFO blocks.
	if err == nil && (info.Mode().IsRegular() || info.IsDir()) {
		var f *os.File
		if f, err = os.Open(path); err == nil {
			f.Close() //nolint:errcheck
		}
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrNotExist):
		return "not found"
	default:
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return pathErr.Err.Error()
		}
		return err.Error()
	}
}
