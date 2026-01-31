package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bernd/vibepit/cmd"
	ctr "github.com/bernd/vibepit/container"
)

func main() {
	app := cmd.RootCommand()

	if err := app.Run(context.Background(), os.Args); err != nil {
		var exitErr *ctr.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
