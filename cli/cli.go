// Package cli provides the command-line interface, argument parsing, and daemon orchestration for loki-prune.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
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

	return c.execute(sigCtx, kctx, out)
}

// RunCLI is an alias for Run for backwards compatibility.
func RunCLI(args []string, out, errOut io.Writer) error {
	return Run(args, out, errOut)
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

	rotators, events, rErr := initRotators(ctx, cfg, c.DryRun)
	if rErr != nil {
		return rErr
	}
	defer func() {
		for _, r := range rotators {
			_ = r.Close()
		}
	}()

	runCycle := func() float64 {
		return runCycleOnce(ctx, rotators)
	}

	initialRatio := runCycle()

	if c.Once {
		appLog.InfoContext(ctx, "Completed single run cycle, exiting.")

		return nil
	}

	return c.RunDaemonAdaptive(
		ctx,
		cfg.Daemon.Interval,
		cfg.Daemon.MinInterval,
		cfg.Daemon.Adaptive,
		initialRatio,
		events,
		runCycle,
	)
}

func initRotators(
	ctx context.Context,
	cfg *config.Config,
	dryRun bool,
) ([]*rotator.Rotator, <-chan struct{}, error) {
	rotators := make([]*rotator.Rotator, 0, len(cfg.Paths))
	combinedEvents := make(chan struct{}, 1)

	for _, p := range cfg.Paths {
		r, rErr := rotator.New(p, cfg.Loki, dryRun)
		if rErr != nil {
			return nil, nil, fmt.Errorf("%w: %s: %w", ErrInitRotator, p.Name, rErr)
		}
		r.StartTracker(ctx)
		rotators = append(rotators, r)

		if trk := r.Tracker(); trk != nil {
			forwardTrackerEvents(ctx, trk.Events(), combinedEvents)
		}
	}

	return rotators, combinedEvents, nil
}

func forwardTrackerEvents(ctx context.Context, in <-chan struct{}, out chan<- struct{}) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
}

func runCycleOnce(ctx context.Context, rotators []*rotator.Rotator) float64 {
	log := logger.FromContext(ctx)
	var maxRatio float64
	for _, r := range rotators {
		if ctx.Err() != nil {
			return maxRatio
		}
		ratio, pErr := r.ProcessWithRatio(ctx)
		if pErr != nil {
			log.ErrorContext(ctx, "Error during rotation process", "err", pErr)
		}
		if ratio > maxRatio {
			maxRatio = ratio
		}
	}

	return maxRatio
}

// ComputeNextInterval calculates the next sleep duration based on quota/disk ratio and adaptive settings.
func ComputeNextInterval(interval, minInterval time.Duration, adaptive bool, ratio float64) time.Duration {
	if !adaptive || minInterval <= 0 || minInterval >= interval {
		return interval
	}

	const threshold = 0.5
	switch {
	case ratio >= 1.0:
		return minInterval
	case ratio > threshold:
		factor := (1.0 - ratio) / threshold
		diff := float64(interval - minInterval)

		return minInterval + time.Duration(factor*diff)
	default:
		return interval
	}
}

// RunDaemon runs a continuous ticker loop executing the provided cycle function until the context is canceled.
func (c *CLI) RunDaemon(ctx context.Context, interval time.Duration, runCycle func()) error {
	return c.RunDaemonAdaptive(ctx, interval, interval, false, 0, nil, func() float64 {
		runCycle()

		return 0
	})
}

// RunDaemonAdaptive runs an adaptive timer loop executing runCycle until context is canceled.
func (c *CLI) RunDaemonAdaptive(
	ctx context.Context,
	interval, minInterval time.Duration,
	adaptive bool,
	initialRatio float64,
	events <-chan struct{},
	runCycle func() float64,
) error {
	appLog := logger.FromContext(ctx)
	currentInterval := ComputeNextInterval(interval, minInterval, adaptive, initialRatio)
	timer := time.NewTimer(currentInterval)
	defer timer.Stop()

	var running sync.Mutex
	var wg sync.WaitGroup
	var latestRatioBits atomic.Uint64
	latestRatioBits.Store(math.Float64bits(initialRatio))

	appLog.InfoContext(ctx, "Entering daemon loop",
		"interval", interval,
		"min_interval", minInterval,
		"adaptive", adaptive,
		"initial_interval", currentInterval,
	)

	triggerCycle := func() {
		if running.TryLock() {
			wg.Go(func() {
				defer running.Unlock()
				ratio := runCycle()
				latestRatioBits.Store(math.Float64bits(ratio))
			})
		} else {
			appLog.WarnContext(ctx, "Previous cycle still in progress, skipping this tick")
		}

		ratio := math.Float64frombits(latestRatioBits.Load())
		currentInterval = ComputeNextInterval(interval, minInterval, adaptive, ratio)
		timer.Reset(currentInterval)
	}

	for {
		select {
		case <-ctx.Done():
			appLog.InfoContext(ctx, "Shutdown signal received, waiting for pending operations...")
			wg.Wait()
			appLog.InfoContext(ctx, "Termination complete.")

			return nil
		case <-timer.C:
			triggerCycle()
		case <-events:
			appLog.DebugContext(ctx, "Realtime filesystem event detected")
			triggerCycle()
		}
	}
}
