package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/cmd"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
)

func main() {
	app := cmd.RootCommand()

	// Force the vibed subcommand when the app is started as "vibed"
	args := os.Args[:]
	self, err := filepath.Abs(args[0])
	if err != nil {
		tui.Error("%v", err)
		os.Exit(1)
	}
	if filepath.Base(self) == "vibed" {
		args = []string{self, "vibed"}
	}

	if err := app.Run(context.Background(), args); err != nil {
		if exitErr, ok := errors.AsType[*ctr.ExitError](err); ok {
			os.Exit(exitErr.Code)
		}
		tui.Error("%v", err)
		os.Exit(1)
	}
}
