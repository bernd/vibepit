package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/internal/runtimeenv"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func RunCommand() *cli.Command {
	return &cli.Command{
		Name:   "run",
		Usage:  "Start the sandbox",
		Flags:  sandboxFlags(),
		Action: RunAction,
	}
}

func RunAction(ctx context.Context, cmd *cli.Command) error {
	tui.PrintHeader()

	projectRoot, u, err := resolveProjectAndUser(cmd)
	if err != nil {
		return err
	}
	workingDir, _, err := resolveProjectWorkingDirectory(projectRoot, cmd.Args().First())
	if err != nil {
		return err
	}
	workingDirEnv := []string{runtimeenv.WorkingDir + "=" + workingDir}

	client, err := ctr.NewClient(ctr.WithDebug(cmd.Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close()

	existing, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if existing != nil {
		tui.Status("Attaching", "to running session in %s", projectRoot)
		return client.ExecSession(ctx, existing.ContainerID, workingDir, workingDirEnv)
	}

	infra, cleanups, err := startSessionInfra(ctx, cmd, client, projectRoot, u, infraOptions{})
	defer runCleanups(cleanups)
	if err != nil {
		return err
	}

	tui.Status("Creating", "sandbox container in %s", projectRoot)
	sandboxConfig := infra.baseSandboxConfig(projectRoot, u)
	sandboxConfig.WorkDir = workingDir
	sandboxConfig.ExtraEnv = workingDirEnv
	sandboxContainer, err := client.CreateSandboxContainer(ctx, sandboxConfig)
	if err != nil {
		return fmt.Errorf("sandbox container: %w", err)
	}
	defer func() {
		tui.Status("Stopping", "sandbox container")
		client.StopAndRemove(ctx, sandboxContainer)
	}()

	tui.Status("Starting", "sandbox container")
	tui.Status("Attaching", "shell session")
	fmt.Println()
	return client.AttachAndStartSession(ctx, sandboxContainer)
}
