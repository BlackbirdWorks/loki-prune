package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/rotator"
	"github.com/blackbirdworks/loki-prune/pkg/version"
)

func main() {
	configPath := flag.String("config", "loki-prune.yaml", "Path to YAML configuration file")
	dryRun := flag.Bool("dry-run", false, "Simulate actions without moving, compressing, or deleting files")
	runOnce := flag.Bool("once", false, "Run evaluation once and exit immediately (useful for crons)")
	showVersion := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("loki-prune version %s (commit %s, built %s)\n", version.BuildVersion, version.Commit, version.BuildDate)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	setupLogging(cfg.Daemon.LogLevel)

	slog.Info("Starting loki-prune",
		"version", version.BuildVersion,
		"dry_run", *dryRun,
		"once", *runOnce,
		"paths_count", len(cfg.Paths),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var rotators []*rotator.Rotator
	for _, p := range cfg.Paths {
		r, err := rotator.New(p, cfg.Loki, *dryRun)
		if err != nil {
			slog.Error("Failed to initialize rotator", "name", p.Name, "err", err)
			os.Exit(1)
		}
		rotators = append(rotators, r)
	}

	runCycle := func() {
		for _, r := range rotators {
			if err := r.Process(ctx); err != nil {
				slog.Error("Error during rotation process", "err", err)
			}
		}
	}

	// Always execute immediate initial cycle
	runCycle()

	if *runOnce {
		slog.Info("Completed single run cycle, exiting.")
		return
	}

	ticker := time.NewTicker(cfg.Daemon.Interval)
	defer ticker.Stop()

	slog.Info("Entering daemon loop", "interval", cfg.Daemon.Interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Shutdown signal received, terminating cleanly.")
			return
		case <-ticker.C:
			runCycle()
		}
	}
}

func setupLogging(levelStr string) {
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
}
