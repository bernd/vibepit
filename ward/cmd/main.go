package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/bernd/vibepit/ward"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "ward",
		Short: "Terminal wrapper with notification overlay",
	}

	root.AddCommand(runCmd())
	root.AddCommand(notifyCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	var socketPath string

	cmd := &cobra.Command{
		Use:   "run -- [command] [args...]",
		Short: "Run a command in the ward PTY wrapper",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()

			w := ward.NewWrapper(ward.Options{
				Command:    args,
				SocketPath: socketPath,
			})

			exitCode, err := w.Run(ctx)
			if err != nil {
				return fmt.Errorf("ward: %w", err)
			}
			os.Exit(exitCode)
			return nil
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix socket path (auto-generated if empty)")
	return cmd
}

func notifyCmd() *cobra.Command {
	var socketPath string
	var timeout int

	cmd := &cobra.Command{
		Use:   "notify [message]",
		Short: "Send a notification to a running ward session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sock := socketPath
			if sock == "" {
				sock = os.Getenv("WARD_SOCKET")
			}
			if sock == "" {
				return fmt.Errorf("no socket path: set --socket or WARD_SOCKET env var")
			}

			message := strings.Join(args, " ")
			dur := time.Duration(timeout) * time.Second
			if err := ward.SendNotification(sock, message, dur); err != nil {
				return fmt.Errorf("notify: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix socket path (uses WARD_SOCKET env var if not set)")
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 3, "notification display duration in seconds")
	return cmd
}
