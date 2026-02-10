package main

import (
	"context"
	"errors"
	"os"

	"github.com/bernd/vibepit/cmd"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
)

func main() {
	app := cmd.RootCommand()

	if err := app.Run(context.Background(), os.Args); err != nil {
		var exitErr *ctr.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		tui.Error("%v", err)
		os.Exit(1)
	}
}
