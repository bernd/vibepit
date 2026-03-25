package cmd

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/bernd/vibepit/sshd"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func VibedCommand() *cli.Command {
	return &cli.Command{
		Name:   "vibed",
		Usage:  "Run the SSH server (internal, runs inside sandbox)",
		Hidden: true,
		Action: VibedAction,
	}
}

func VibedAction(ctx context.Context, cmd *cli.Command) error {
	hostKey, err := os.ReadFile("/etc/vibepit/sshd/host-key")
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	authorizedKey := os.Getenv("VIBEPIT_SSH_PUBKEY")
	if authorizedKey == "" {
		return fmt.Errorf("VIBEPIT_SSH_PUBKEY not set")
	}

	srv, err := sshd.NewServer(sshd.Config{
		HostKeyPEM:    hostKey,
		AuthorizedKey: []byte(authorizedKey),
	})
	if err != nil {
		return fmt.Errorf("create ssh server: %w", err)
	}

	listener, err := net.Listen("tcp", "0.0.0.0:2222")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	tui.Status("Listening", "on :2222")
	return srv.Serve(listener)
}
