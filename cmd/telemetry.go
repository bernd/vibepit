package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

func TelemetryCommand() *cli.Command {
	return &cli.Command{
		Name:     "telemetry",
		Usage:    "Stream raw OTLP events and metrics as JSON lines",
		Category: "Utilities",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "events",
				Value: true,
				Usage: "Stream telemetry events",
			},
			&cli.BoolFlag{
				Name:  "metrics",
				Value: true,
				Usage: "Stream metric snapshots",
			},
			&cli.BoolFlag{
				Name:  "raw",
				Usage: "Include raw OTLP payloads",
			},
			&cli.StringFlag{
				Name:  "agent",
				Usage: "Filter by agent name",
			},
			sessionFlag,
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			showEvents := cmd.Bool("events")
			showMetrics := cmd.Bool("metrics")
			raw := cmd.Bool("raw")
			agent := cmd.String("agent")

			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			var cursor uint64
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()

			// Initial poll before first tick.
			cursor = pollTelemetry(ctx, client, enc, cursor, agent, raw, showEvents, showMetrics)

			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					cursor = pollTelemetry(ctx, client, enc, cursor, agent, raw, showEvents, showMetrics)
				}
			}
		},
	}
}

func pollTelemetry(ctx context.Context, client *ControlClient, enc *json.Encoder, cursor uint64, agent string, raw, showEvents, showMetrics bool) uint64 {
	if ctx.Err() != nil {
		return cursor
	}

	if showEvents {
		events, err := client.TelemetryEventsAfter(cursor, agent, raw)
		if err == nil {
			for _, e := range events {
				enc.Encode(e)
				if e.ID > cursor {
					cursor = e.ID
				}
			}
		}
	}

	if showMetrics {
		metrics, err := client.TelemetryMetrics(raw)
		if err == nil {
			for _, m := range metrics {
				if agent != "" && m.Agent != agent {
					continue
				}
				enc.Encode(struct {
					Type string `json:"type"`
					proxy.MetricSummary
				}{Type: "metric", MetricSummary: m})
			}
		}
	}

	return cursor
}
