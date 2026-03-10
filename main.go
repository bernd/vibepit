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
		if exitErr, ok := errors.AsType[*ctr.ExitError](err); ok {
			os.Exit(exitErr.Code)
		}
		tui.Error("%v", err)
		os.Exit(1)
	}
}
