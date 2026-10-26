package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/disk"
	"github.com/blackbirdworks/loki-prune/pkg/logger"
	"github.com/blackbirdworks/loki-prune/pkg/loki"
	"github.com/blackbirdworks/loki-prune/pkg/rotator"
)

const (
	percentFactor = 100.0
)

// LokiClient defines the interface for Loki synchronization calls.
type LokiClient interface {
	Flush(ctx context.Context) error
	Reload(ctx context.Context) error
}

// Option configures Service parameters.
type Option func(*Service)

// WithDryRun enables or disables dry-run simulation mode.
func WithDryRun(dryRun bool) Option {
	return func(s *Service) {
		s.dryRun = dryRun
	}
}

// WithOnce sets whether the service runs one evaluation cycle and terminates.
func WithOnce(once bool) Option {
	return func(s *Service) {
		s.once = once
	}
}

// WithLokiClient overrides the Loki client with a custom implementation.
func WithLokiClient(client LokiClient) Option {
	return func(s *Service) {
		s.lokiClient = client
	}
}

// WithDebounceDelay configures the filesystem event cooldown window.
func WithDebounceDelay(d time.Duration) Option {
	return func(s *Service) {
		s.debounceDelay = d
	}
}

// Service is the stateful daemon that tracks filesystem changes, coordinates rotation,
// compression, retention, and adapts its scheduling loop.
type Service struct {
	cfg           *config.Config
	lokiClient    LokiClient
	trackers      map[string]*Tracker
	compressors   map[string]*compressor.Compressor
	debounceDelay time.Duration
	dryRun        bool
	once          bool
	cycleMu       sync.Mutex
}

// New creates a new stateful daemon Service instance.
func New(cfg *config.Config, opts ...Option) (*Service, error) {
	s := &Service{
		cfg:         cfg,
		trackers:    make(map[string]*Tracker),
		compressors: make(map[string]*compressor.Compressor),
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.lokiClient == nil {
		s.lokiClient = loki.NewClient(cfg.Loki.URL, cfg.Loki.Timeout)
	}

	for _, p := range cfg.Paths {
		comp, err := compressor.New(p.Rotation.Type, p.Rotation.Level)
		if err != nil {
			return nil, fmt.Errorf("initializing compressor for %s: %w", p.Name, err)
		}
		s.compressors[p.Name] = comp

		if cfg.Daemon.Mode == config.ModeFSEvent {
			cooldown := cfg.Daemon.Cooldown
			if s.debounceDelay > 0 {
				cooldown = s.debounceDelay
			}
			trk, trkErr := NewTracker(p.Path, p.ArchiveDir, cooldown)
			if trkErr != nil {
				return nil, fmt.Errorf("initializing tracker for %s: %w", p.Name, trkErr)
			}
			s.trackers[p.Name] = trk
		}
	}

	return s, nil
}

// Run starts tracking and executes either a single run or the continuous daemon loop.
func (s *Service) Run(ctx context.Context) error {
	log := logger.FromContext(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var events <-chan struct{}
	if s.cfg.Daemon.Mode == config.ModeFSEvent {
		combinedEvents, cleanup := s.initEventPipeline(runCtx)
		defer cleanup()
		events = combinedEvents
	}

	initialRatio := s.RunCycle(runCtx)

	if s.once {
		log.InfoContext(runCtx, "Completed single run cycle, exiting.")

		return nil
	}

	return s.runDaemonAdaptive(runCtx, initialRatio, events)
}

func (s *Service) initEventPipeline(ctx context.Context) (<-chan struct{}, func()) {
	combinedEvents := make(chan struct{}, 1)
	var forwarderWg sync.WaitGroup

	for _, p := range s.cfg.Paths {
		trk := s.trackers[p.Name]
		if trk == nil {
			continue
		}
		trk.Start(ctx)
		_, _, _ = trk.Sync()

		forwarderWg.Add(1)
		go s.forwardEvents(ctx, trk.Events(), combinedEvents, &forwarderWg)
	}

	cleanup := func() {
		for _, trk := range s.trackers {
			_ = trk.Close()
		}
		forwarderWg.Wait()
		close(combinedEvents)
	}

	return combinedEvents, cleanup
}

func (s *Service) forwardEvents(
	ctx context.Context,
	in <-chan struct{},
	out chan<- struct{},
	wg *sync.WaitGroup,
) {
	defer wg.Done()
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
}

// RunCycle executes a full evaluation, rotation, compression, and pruning cycle across all configured paths.
func (s *Service) RunCycle(ctx context.Context) float64 {
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()

	log := logger.FromContext(ctx)
	var maxRatio float64

	for _, p := range s.cfg.Paths {
		if ctx.Err() != nil {
			return maxRatio
		}

		ratio, err := s.processPath(ctx, p)
		if err != nil {
			log.ErrorContext(ctx, "Error during rotation process", "name", p.Name, "err", err)
		}
		if ratio > maxRatio {
			maxRatio = ratio
		}
	}

	return maxRatio
}

func (s *Service) processPath(ctx context.Context, p config.PathConfig) (float64, error) {
	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "Evaluating path", "name", p.Name, "path", p.Path)

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		log.WarnContext(ctx, "Path does not exist, skipping", "path", p.Path)

		return 0, nil
	}

	currentSize, fileCount, sizeErr := s.getPathSize(p)
	if sizeErr != nil {
		return 0, sizeErr
	}

	log.InfoContext(ctx, "Path size status",
		"name", p.Name,
		"current", config.FormatBytes(currentSize),
		"trigger", config.FormatBytes(p.TriggerSizeBytes),
		"files", fileCount,
	)

	var ratio float64
	if p.TriggerSizeBytes > 0 {
		ratio = float64(currentSize) / float64(p.TriggerSizeBytes)
	}

	rotated := false
	if currentSize > p.TriggerSizeBytes {
		log.WarnContext(ctx, "Path size exceeded trigger limit, beginning rotation",
			"name", p.Name,
			"current", config.FormatBytes(currentSize),
			"trigger", config.FormatBytes(p.TriggerSizeBytes),
		)

		if rotErr := s.rotatePath(ctx, p, currentSize); rotErr != nil {
			return ratio, fmt.Errorf("executing rotation on %s: %w", p.Name, rotErr)
		}
		rotated = true
	}

	ratio, guardErr := s.checkDiskGuard(ctx, p, currentSize, rotated, ratio)
	if guardErr != nil {
		return ratio, guardErr
	}

	if p.Rotation.Compress {
		comp := s.compressors[p.Name]
		if compErr := rotator.Compress(ctx, p.ArchiveDir, p.Rotation, comp, s.dryRun); compErr != nil {
			log.ErrorContext(ctx, "Error during compression cycle", "name", p.Name, "err", compErr)
		}
	}

	if p.Rotation.MaxRotations > 0 {
		if pruneErr := rotator.EnforceRetention(ctx, p.ArchiveDir, p.Rotation.MaxRotations, s.dryRun); pruneErr != nil {
			log.ErrorContext(ctx, "Error enforcing max rotations", "name", p.Name, "err", pruneErr)
		}
	}

	return ratio, nil
}

func (s *Service) getPathSize(p config.PathConfig) (int64, int, error) {
	trk := s.trackers[p.Name]
	if trk == nil {
		sz, count, err := rotator.CalculateDirSize(p.Path, p.ArchiveDir)
		if err != nil {
			return 0, 0, fmt.Errorf("calculating directory size for %s: %w", p.Path, err)
		}

		return sz, count, nil
	}

	currentSize, fileCount := trk.Size()
	if currentSize == 0 && fileCount == 0 {
		sz, count, err := trk.Sync()
		if err != nil {
			return 0, 0, fmt.Errorf("syncing tracker for %s: %w", p.Name, err)
		}

		return sz, count, nil
	}

	return currentSize, fileCount, nil
}

func (s *Service) checkDiskGuard(
	ctx context.Context,
	p config.PathConfig,
	currentSize int64,
	rotated bool,
	currentRatio float64,
) (float64, error) {
	usage, err := disk.Usage(p.Path)
	if err != nil {
		return currentRatio, fmt.Errorf("checking disk usage for %s: %w", p.Path, err)
	}

	guardThreshold := p.DiskGuardPercent
	if guardThreshold <= 0 {
		guardThreshold = 90.0
	}

	diskRatio := usage.UsedPercent / percentFactor
	ratio := math.Max(currentRatio, diskRatio)

	if usage.UsedPercent >= guardThreshold && !rotated {
		log := logger.FromContext(ctx)
		log.WarnContext(ctx, "Disk guard threshold exceeded, triggering emergency rotation",
			"name", p.Name,
			"used_percent", fmt.Sprintf("%.2f%%", usage.UsedPercent),
			"guard_threshold", fmt.Sprintf("%.2f%%", guardThreshold),
		)
		if rotErr := s.rotatePath(ctx, p, currentSize); rotErr != nil {
			return ratio, fmt.Errorf("executing emergency disk guard rotation on %s: %w", p.Name, rotErr)
		}
	}

	return ratio, nil
}

func (s *Service) rotatePath(ctx context.Context, p config.PathConfig, currentSize int64) error {
	log := logger.FromContext(ctx)

	log.InfoContext(ctx, "Flushing Loki before rotating")
	if !s.dryRun {
		if err := s.lokiClient.Flush(ctx); err != nil {
			log.WarnContext(ctx, "Loki flush failed (proceeding with caution)", "err", err)
		} else {
			log.InfoContext(ctx, "Loki flush completed successfully")
		}
	}

	var items []rotator.FileItem
	trk := s.trackers[p.Name]
	if trk != nil {
		items = trk.OldestItems()
	}

	bytesToFree := currentSize - p.TargetSizeBytes
	res, rotErr := rotator.Rotate(ctx, rotator.RotateOptions{
		SourceDir:   p.Path,
		ArchiveDir:  p.ArchiveDir,
		Items:       items,
		BytesToFree: bytesToFree,
		DryRun:      s.dryRun,
	})
	if rotErr != nil {
		return rotErr
	}

	if trk != nil && !s.dryRun {
		for _, item := range res.MovedItems {
			trk.Remove(item.Path)
		}
	}

	log.InfoContext(ctx, "Synchronizing Loki after rotation")
	if !s.dryRun {
		if err := s.lokiClient.Reload(ctx); err != nil {
			log.WarnContext(ctx, "Loki reload after rotation returned error", "err", err)
		}
	}

	return nil
}

func (s *Service) runDaemonAdaptive(ctx context.Context, initialRatio float64, events <-chan struct{}) error {
	appLog := logger.FromContext(ctx)
	interval := s.cfg.Daemon.Interval
	minInterval := s.cfg.Daemon.MinInterval
	adaptive := s.cfg.Daemon.Adaptive

	currentInterval := ComputeNextInterval(interval, minInterval, adaptive, initialRatio)
	timer := time.NewTimer(currentInterval)
	defer timer.Stop()

	appLog.InfoContext(ctx, "Entering daemon loop",
		"mode", s.cfg.Daemon.Mode,
		"interval", interval,
		"min_interval", minInterval,
		"adaptive", adaptive,
		"initial_interval", currentInterval,
	)

	triggerCycle := func() {
		ratio := s.RunCycle(ctx)
		currentInterval = ComputeNextInterval(interval, minInterval, adaptive, ratio)
		timer.Reset(currentInterval)
	}

	for {
		select {
		case <-ctx.Done():
			appLog.InfoContext(ctx, "Shutdown signal received, terminating daemon loop...")

			return nil
		case <-timer.C:
			triggerCycle()
		case _, ok := <-events:
			if !ok {
				events = nil

				break
			}
			appLog.DebugContext(ctx, "Realtime debounced filesystem event detected")
			triggerCycle()
		}
	}
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
