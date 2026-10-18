// Command scaler runs GitHub Actions jobs in KubeVirt VMs: one ephemeral VM per job.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/milanoid-labs/kubevirt-scaleset/internal/config"
	"github.com/milanoid-labs/kubevirt-scaleset/internal/github"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("scaler failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg.LogLevel)
	log.Info("starting", "version", version, "url", cfg.GitHubConfigURL, "scaleSet", cfg.ScaleSetName,
		"minRunners", cfg.MinRunners, "maxRunners", cfg.MaxRunners)

	// SIGTERM is what Kubernetes sends on pod shutdown, SIGINT is Ctrl-C locally.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := github.NewClient(cfg.GitHubConfigURL, cfg.AppClientID, cfg.AppInstallationID, cfg.AppPrivateKey, version)
	if err != nil {
		return fmt.Errorf("github client: %w", err)
	}

	scaleSet, err := github.EnsureScaleSet(ctx, client, cfg.RunnerGroup, cfg.ScaleSetName, log)
	if err != nil {
		return err
	}

	// Next: open the message session and create one VM per assigned job.
	log.Info("scale set ready", "id", scaleSet.ID)
	return nil
}

// newLogger logs JSON, which Loki and kubectl logs | jq both read well.
func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
