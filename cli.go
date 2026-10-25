package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	kongyaml "github.com/alecthomas/kong-yaml"

	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/logger"
	"github.com/blackbirdworks/loki-prune/pkg/rotator"
	"github.com/blackbirdworks/loki-prune/pkg/version"
)

var (
	// ErrInitRotator is returned when a path rotator fails to initialize.
	ErrInitRotator = errors.New("failed to initialize rotator")
	// ErrCLIExit is returned when Kong requests a non-zero exit during argument processing.
	ErrCLIExit = errors.New("cli execution terminated with non-zero exit code")
)

// CLI represents the command-line options and flags parsed by Kong.
type CLI struct {
	Config  string           `default:"loki-prune.yaml" help:"Path to YAML config file."         short:"c" type:"path"`
	DryRun  bool             `                          help:"Simulate actions without changes."`
	Once    bool             `                          help:"Run evaluation once and exit."`
	Version kong.VersionFlag `                          help:"Print version and exit."           short:"v"`
}

// RunCLI initializes Kong, parses arguments, and executes the CLI application.
func RunCLI(args []string, out, errOut io.Writer) error {
	var cli CLI
	var exited bool
	var exitCode int

	parser, err := kong.New(
		&cli,
		kong.Name("loki-prune"),
		kong.Description("Automated storage-quota rotator and compression engine for Grafana Loki"),
		kong.Configuration(kongyaml.Loader),
		kong.UsageOnError(),
		kong.Writers(out, errOut),
		kong.Exit(func(code int) {
			exited = true
			exitCode = code
		}),
		kong.Vars{
			"version": fmt.Sprintf("%s (commit %s, built %s)", version.BuildVersion, version.Commit, version.BuildDate),
		},
	)
	if err != nil {
		return fmt.Errorf("creating CLI parser: %w", err)
	}

	kctx, err := parser.Parse(args)
	if exited {
		if exitCode != 0 {
			return ErrCLIExit
		}

		return nil
	}
	if err != nil {
		return fmt.Errorf("parsing CLI arguments: %w", err)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.execute(sigCtx, kctx, out)
}

func (c *CLI) execute(ctx context.Context, _ *kong.Context, out io.Writer) error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	appLog := logger.New(out, cfg.Daemon.LogType, logger.ParseLevel(cfg.Daemon.LogLevel))
	ctx = logger.Into(ctx, appLog)

	appLog.InfoContext(ctx, "Starting loki-prune",
		"version", version.BuildVersion,
		"dry_run", c.DryRun,
		"once", c.Once,
		"paths_count", len(cfg.Paths),
	)

	rotators := make([]*rotator.Rotator, 0, len(cfg.Paths))
	for _, p := range cfg.Paths {
		r, rErr := rotator.New(p, cfg.Loki, c.DryRun)
		if rErr != nil {
			return fmt.Errorf("%w: %s: %w", ErrInitRotator, p.Name, rErr)
		}
		rotators = append(rotators, r)
	}

	runCycle := func() {
		for _, r := range rotators {
			if ctx.Err() != nil {
				return
			}
			if pErr := r.Process(ctx); pErr != nil {
				appLog.ErrorContext(ctx, "Error during rotation process", "err", pErr)
			}
		}
	}

	runCycle()

	if c.Once {
		appLog.InfoContext(ctx, "Completed single run cycle, exiting.")

		return nil
	}

	return c.runDaemon(ctx, cfg.Daemon.Interval, runCycle)
}

func (c *CLI) runDaemon(ctx context.Context, interval time.Duration, runCycle func()) error {
	appLog := logger.FromContext(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var running sync.Mutex
	var wg sync.WaitGroup

	appLog.InfoContext(ctx, "Entering daemon loop", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			appLog.InfoContext(ctx, "Shutdown signal received, waiting for pending operations...")
			wg.Wait()
			appLog.InfoContext(ctx, "Termination complete.")

			return nil
		case <-ticker.C:
			if running.TryLock() {
				wg.Go(func() {
					defer running.Unlock()
					runCycle()
				})
			} else {
				appLog.WarnContext(ctx, "Previous cycle still in progress, skipping this tick")
			}
		}
	}
}
