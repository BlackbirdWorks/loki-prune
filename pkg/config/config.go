// Package config handles parsing and validation of loki-prune configuration files.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/blackbirdworks/loki-prune/pkg/bytesize"
)

var (
	// ErrNoPaths is returned when no paths are defined in the config.
	ErrNoPaths = errors.New("no paths defined in config")
	// ErrMissingPath is returned when a path item is missing the 'path' attribute.
	ErrMissingPath = errors.New("missing required 'path'")
	// ErrMissingTriggerSize is returned when a path item is missing 'trigger_size'.
	ErrMissingTriggerSize = errors.New("missing required 'trigger_size'")
	// ErrTargetGteTrigger is returned when target_size is greater than or equal to trigger_size.
	ErrTargetGteTrigger = errors.New("target_size must be less than trigger_size")
	// ErrUnsupportedCompression is returned for unknown compression types.
	ErrUnsupportedCompression = errors.New("unsupported compression type")
	// ErrInvalidSizeFormat is returned when a byte size string cannot be parsed.
	ErrInvalidSizeFormat = bytesize.ErrInvalidFormat
	// ErrUnknownUnit is returned when a byte unit is unrecognized.
	ErrUnknownUnit = bytesize.ErrUnknownUnit
	// ErrInvalidDuration is returned when a time duration cannot be parsed.
	ErrInvalidDuration = errors.New("invalid duration")
	// ErrInvalidDaemonMode is returned when daemon mode is neither 'poll' nor 'fsevent'.
	ErrInvalidDaemonMode = errors.New("invalid daemon mode, must be 'poll' or 'fsevent'")
)

const (
	// ModePoll runs evaluation on scheduled intervals without filesystem watchers.
	ModePoll = "poll"
	// ModeFSEvent runs evaluation triggered by filesystem events with a cooldown window.
	ModeFSEvent = "fsevent"

	defaultCooldown         = 5 * time.Second
	defaultTargetRatio      = 0.8
	defaultLokiTimeout      = 30 * time.Second
	defaultInterval         = 5 * time.Minute
	defaultMinInterval      = 5 * time.Second
	defaultDiskGuardPercent = 90.0
)

// Config represents the top-level loki-prune configuration.
type Config struct {
	Paths  []PathConfig `yaml:"paths"`
	Loki   LokiConfig   `yaml:"loki"`
	Daemon DaemonConfig `yaml:"daemon"`
}

// LokiConfig holds settings for communicating with the Loki API.
type LokiConfig struct {
	URL               string        `yaml:"url"`
	TimeoutStr        string        `yaml:"timeout"`
	Timeout           time.Duration `yaml:"-"`
	FlushBeforeRotate bool          `yaml:"flush_before_rotate"`
	ReloadAfterRotate bool          `yaml:"reload_after_rotate"`
}

// DaemonConfig holds service and scheduling options.
type DaemonConfig struct {
	AdaptiveRaw    *bool         `yaml:"adaptive"`
	Mode           string        `yaml:"mode"`     // "poll" (default) or "fsevent"
	CooldownStr    string        `yaml:"cooldown"` // event cooldown (default "5s")
	IntervalStr    string        `yaml:"interval"`
	MinIntervalStr string        `yaml:"min_interval"`
	LogLevel       string        `yaml:"log_level"`
	LogType        string        `yaml:"log_type"`
	Cooldown       time.Duration `yaml:"-"`
	Interval       time.Duration `yaml:"-"`
	MinInterval    time.Duration `yaml:"-"`
	Adaptive       bool          `yaml:"-"`
}

// PathConfig defines rotation rules for a specific directory.
type PathConfig struct {
	Name             string         `yaml:"name"`
	Path             string         `yaml:"path"`
	ArchiveDir       string         `yaml:"archive_dir"`
	TriggerSizeStr   string         `yaml:"trigger_size"`
	TargetSizeStr    string         `yaml:"target_size"`
	Rotation         RotationConfig `yaml:"rotation"`
	DiskGuardPercent float64        `yaml:"disk_guard_percent"`
	TriggerSizeBytes int64          `yaml:"-"`
	TargetSizeBytes  int64          `yaml:"-"`
}

// RotationConfig defines compression and retention policies for rotated items.
type RotationConfig struct {
	Type         string      `yaml:"type"`
	Delay        DelayConfig `yaml:"delay"`
	MaxRotations int         `yaml:"max_rotations"`
	Level        int         `yaml:"level"`
	Compress     bool        `yaml:"compress"`
}

// DelayConfig controls when compression takes place.
type DelayConfig struct {
	AgeStr    string        `yaml:"age"`
	Age       time.Duration `yaml:"-"`
	Rotations int           `yaml:"rotations"`
}

// Load reads and parses a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}

	if err = cfg.validateAndSetDefaults(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validateAndSetDefaults() error {
	if err := c.validateLoki(); err != nil {
		return err
	}

	if err := c.validateDaemon(); err != nil {
		return err
	}

	if len(c.Paths) == 0 {
		return ErrNoPaths
	}

	for i := range c.Paths {
		if err := c.validatePath(i); err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) validateLoki() error {
	if c.Loki.URL == "" {
		c.Loki.URL = "http://127.0.0.1:3100"
	}
	c.Loki.URL = strings.TrimRight(c.Loki.URL, "/")

	if c.Loki.TimeoutStr == "" {
		c.Loki.Timeout = defaultLokiTimeout

		return nil
	}

	d, err := time.ParseDuration(c.Loki.TimeoutStr)
	if err != nil {
		return fmt.Errorf("%w: loki.timeout: %w", ErrInvalidDuration, err)
	}
	c.Loki.Timeout = d

	return nil
}

func (c *Config) validateDaemon() error {
	if err := c.validateDaemonMode(); err != nil {
		return err
	}

	if err := c.validateDaemonIntervals(); err != nil {
		return err
	}

	c.validateDaemonLogging()

	return nil
}

func (c *Config) validateDaemonMode() error {
	mode := strings.ToLower(strings.TrimSpace(c.Daemon.Mode))
	if mode == "" {
		mode = ModePoll
	}
	if mode != ModePoll && mode != ModeFSEvent {
		return fmt.Errorf("%w: %q", ErrInvalidDaemonMode, c.Daemon.Mode)
	}
	c.Daemon.Mode = mode

	if c.Daemon.CooldownStr == "" {
		c.Daemon.Cooldown = defaultCooldown

		return nil
	}

	cd, err := time.ParseDuration(c.Daemon.CooldownStr)
	if err != nil {
		return fmt.Errorf("%w: daemon.cooldown: %w", ErrInvalidDuration, err)
	}
	c.Daemon.Cooldown = cd

	return nil
}

func (c *Config) validateDaemonIntervals() error {
	if c.Daemon.IntervalStr == "" {
		c.Daemon.Interval = defaultInterval
	} else {
		d, err := time.ParseDuration(c.Daemon.IntervalStr)
		if err != nil {
			return fmt.Errorf("%w: daemon.interval: %w", ErrInvalidDuration, err)
		}
		c.Daemon.Interval = d
	}

	if c.Daemon.MinIntervalStr == "" {
		c.Daemon.MinInterval = defaultMinInterval
	} else {
		d, err := time.ParseDuration(c.Daemon.MinIntervalStr)
		if err != nil {
			return fmt.Errorf("%w: daemon.min_interval: %w", ErrInvalidDuration, err)
		}
		c.Daemon.MinInterval = d
	}

	if c.Daemon.AdaptiveRaw != nil {
		c.Daemon.Adaptive = *c.Daemon.AdaptiveRaw
	} else {
		c.Daemon.Adaptive = true
	}

	return nil
}

func (c *Config) validateDaemonLogging() {
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		c.Daemon.LogLevel = envLevel
	} else if c.Daemon.LogLevel == "" {
		c.Daemon.LogLevel = "info"
	}

	if envType := os.Getenv("LOG_TYPE"); envType != "" {
		c.Daemon.LogType = envType
	} else if c.Daemon.LogType == "" {
		c.Daemon.LogType = "text"
	}
}

func (c *Config) validatePath(idx int) error {
	p := &c.Paths[idx]
	if p.Name == "" {
		p.Name = fmt.Sprintf("path-%d", idx+1)
	}
	if p.Path == "" {
		return fmt.Errorf("path[%d] (%s): %w", idx, p.Name, ErrMissingPath)
	}

	if err := c.validatePathSizes(idx); err != nil {
		return err
	}

	if p.ArchiveDir == "" {
		p.ArchiveDir = filepath.Join(p.Path, "archive")
	}

	if p.DiskGuardPercent <= 0 {
		p.DiskGuardPercent = defaultDiskGuardPercent
	}

	return c.validateCompression(idx)
}

func (c *Config) validatePathSizes(idx int) error {
	p := &c.Paths[idx]
	if p.TriggerSizeStr == "" {
		return fmt.Errorf("path[%d] (%s): %w", idx, p.Name, ErrMissingTriggerSize)
	}

	bytes, err := ParseBytes(p.TriggerSizeStr)
	if err != nil {
		return fmt.Errorf("path[%d] (%s) invalid trigger_size: %w", idx, p.Name, err)
	}
	p.TriggerSizeBytes = bytes

	if p.TargetSizeStr != "" {
		tBytes, tErr := ParseBytes(p.TargetSizeStr)
		if tErr != nil {
			return fmt.Errorf("path[%d] (%s) invalid target_size: %w", idx, p.Name, tErr)
		}
		p.TargetSizeBytes = tBytes
	} else {
		p.TargetSizeBytes = int64(float64(p.TriggerSizeBytes) * defaultTargetRatio)
	}

	if p.TargetSizeBytes >= p.TriggerSizeBytes {
		return fmt.Errorf("path[%d] (%s): %w", idx, p.Name, ErrTargetGteTrigger)
	}

	return nil
}

func (c *Config) validateCompression(idx int) error {
	p := &c.Paths[idx]
	if p.Rotation.Type == "" {
		p.Rotation.Type = "zstd"
	}
	p.Rotation.Type = strings.ToLower(p.Rotation.Type)

	switch p.Rotation.Type {
	case "zstd", "gzip", "zip", "none":
	default:
		return fmt.Errorf("path[%d] (%s) %w: %s", idx, p.Name, ErrUnsupportedCompression, p.Rotation.Type)
	}

	if p.Rotation.Delay.AgeStr != "" {
		age, err := time.ParseDuration(p.Rotation.Delay.AgeStr)
		if err != nil {
			return fmt.Errorf("path[%d] (%s) %w: delay.age: %w", idx, p.Name, ErrInvalidDuration, err)
		}
		p.Rotation.Delay.Age = age
	}

	return nil
}

// ParseBytes converts human-friendly byte strings (e.g. "48GB", "500MB", "10GiB") into raw bytes.
func ParseBytes(s string) (int64, error) {
	return bytesize.Parse(s)
}

// FormatBytes formats raw byte count into human-friendly string (e.g. "48.25 GiB").
func FormatBytes(b int64) string {
	return bytesize.Format(b)
}
