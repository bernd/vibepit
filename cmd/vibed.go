package cmd

import (
	"context"
	"fmt"
	"net"
	"os"

	ctr "github.com/bernd/vibepit/container"
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
	hostKey, err := os.ReadFile(ctr.SSHHostKeyPath)
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	authorizedKey := os.Getenv(ctr.SSHPubKeyEnv)
	if authorizedKey == "" {
		return fmt.Errorf("%s not set", ctr.SSHPubKeyEnv)
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
	go func() {
		<-ctx.Done()
		srv.Close() //nolint:errcheck
	}()
	return srv.Serve(listener)
}
