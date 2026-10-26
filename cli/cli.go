// Package cli provides the command-line interface and argument parsing for loki-prune.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	kongyaml "github.com/alecthomas/kong-yaml"

	"github.com/blackbirdworks/loki-prune/internal/service"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/logger"
	"github.com/blackbirdworks/loki-prune/pkg/version"
)

var (
	// ErrCLIExit is returned when Kong requests a non-zero exit during argument processing.
	ErrCLIExit = errors.New("cli execution terminated with non-zero exit code")
)

// CLI represents the command-line options and flags parsed by Kong.
type CLI struct {
	Config  string           `default:"loki-prune.yaml" help:"Path to YAML config file."          short:"c" type:"path"`
	Mode    string           `                          help:"Daemon mode ('poll' or 'fsevent')."`
	DryRun  bool             `                          help:"Simulate actions without changes."`
	Once    bool             `                          help:"Run evaluation once and exit."`
	Version kong.VersionFlag `                          help:"Print version and exit."            short:"v"`
}

// Run initializes Kong, parses arguments, and executes the CLI application.
func Run(args []string, out, errOut io.Writer) error {
	var c CLI
	var exited bool
	var exitCode int

	parser, err := kong.New(
		&c,
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

	_, err = parser.Parse(args)
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

	return c.execute(sigCtx, out)
}

// RunCLI is an alias for Run for backwards compatibility.
func RunCLI(args []string, out, errOut io.Writer) error {
	return Run(args, out, errOut)
}

func (c *CLI) execute(ctx context.Context, out io.Writer) error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if c.Mode != "" {
		cfg.Daemon.Mode = c.Mode
	}

	appLog := logger.New(out, cfg.Daemon.LogType, logger.ParseLevel(cfg.Daemon.LogLevel))
	ctx = logger.Into(ctx, appLog)

	appLog.InfoContext(ctx, "Starting loki-prune",
		"version", version.BuildVersion,
		"dry_run", c.DryRun,
		"once", c.Once,
		"paths_count", len(cfg.Paths),
	)

	svc, err := service.New(
		cfg,
		service.WithDryRun(c.DryRun),
		service.WithOnce(c.Once),
	)
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}

	return svc.Run(ctx)
}
