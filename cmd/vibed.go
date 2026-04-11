package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/session"
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
	// Run the same shell initialization as entrypoint.sh so the home
	// directory and linuxbrew volume are ready before accepting SSH sessions.
	initCmd := exec.Command("bash", "-c",
		"source /etc/vibepit/lib.sh && source /etc/vibepit/entrypoint-lib.sh && migrate_linuxbrew_volume && init_home")
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("sandbox init: %w", err)
	}

	hostKey, err := os.ReadFile(ctr.SSHHostKeyPath)
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}

	authorizedKey := os.Getenv(ctr.SSHPubKeyEnv)
	if authorizedKey == "" {
		return fmt.Errorf("%s not set", ctr.SSHPubKeyEnv)
	}

	mgr := session.NewManager(50)
	mgr.SetStateFilePath(ctr.SessionStatePath)

	srv, err := sshd.NewServer(sshd.Config{
		HostKeyPEM:    hostKey,
		AuthorizedKey: []byte(authorizedKey),
		Sessions:      mgr,
	})
	if err != nil {
		return fmt.Errorf("create ssh server: %w", err)
	}

	listener, err := net.Listen("tcp", "0.0.0.0:2222")
	if err != nil {
		srv.Close() //nolint:errcheck
		return fmt.Errorf("listen: %w", err)
	}

	tui.Status("Listening", "on :2222")
	go func() {
		<-ctx.Done()
		srv.Close() //nolint:errcheck
	}()
	return srv.Serve(listener)
}
