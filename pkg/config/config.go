// Package config handles parsing and validation of loki-prune configuration files.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	ErrInvalidSizeFormat = errors.New("invalid size format")
	// ErrUnknownUnit is returned when a byte unit is unrecognized.
	ErrUnknownUnit = errors.New("unknown unit")
	// ErrInvalidDuration is returned when a time duration cannot be parsed.
	ErrInvalidDuration = errors.New("invalid duration")
)

const (
	defaultTargetRatio  = 0.8
	defaultLokiTimeout  = 30 * time.Second
	defaultInterval     = 5 * time.Minute
	regexExpectedGroups = 3

	unitKibi = 1024
	unitKilo = 1000
	unitMebi = 1024 * 1024
	unitMega = 1000 * 1000
	unitGibi = 1024 * 1024 * 1024
	unitGiga = 1000 * 1000 * 1000
	unitTebi = 1024 * 1024 * 1024 * 1024
	unitTera = 1000 * 1000 * 1000 * 1000
)

// Config represents the top-level loki-prune configuration.
type Config struct {
	Daemon DaemonConfig `yaml:"daemon"`
	Paths  []PathConfig `yaml:"paths"`
	Loki   LokiConfig   `yaml:"loki"`
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
	IntervalStr string        `yaml:"interval"`
	LogLevel    string        `yaml:"log_level"`
	LogType     string        `yaml:"log_type"`
	Interval    time.Duration `yaml:"-"`
}

// PathConfig defines rotation rules for a specific directory.
type PathConfig struct {
	Name             string         `yaml:"name"`
	Path             string         `yaml:"path"`
	ArchiveDir       string         `yaml:"archive_dir"`
	TriggerSizeStr   string         `yaml:"trigger_size"`
	TargetSizeStr    string         `yaml:"target_size"`
	Rotation         RotationConfig `yaml:"rotation"`
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
	if c.Daemon.IntervalStr == "" {
		c.Daemon.Interval = defaultInterval
	} else {
		d, err := time.ParseDuration(c.Daemon.IntervalStr)
		if err != nil {
			return fmt.Errorf("%w: daemon.interval: %w", ErrInvalidDuration, err)
		}
		c.Daemon.Interval = d
	}

	if c.Daemon.LogLevel == "" {
		if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
			c.Daemon.LogLevel = envLevel
		} else {
			c.Daemon.LogLevel = "info"
		}
	}

	if c.Daemon.LogType == "" {
		if envType := os.Getenv("LOG_TYPE"); envType != "" {
			c.Daemon.LogType = envType
		} else {
			c.Daemon.LogType = "text"
		}
	}

	return nil
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
	case "zstd", "gzip", "none":
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

var sizeRegex = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-z]*)\s*$`)

// ParseBytes converts human-friendly byte strings (e.g. "48GB", "500MB", "10GiB") into raw bytes.
func ParseBytes(s string) (int64, error) {
	matches := sizeRegex.FindStringSubmatch(strings.TrimSpace(s))
	if len(matches) != regexExpectedGroups {
		return 0, fmt.Errorf("%w: %q", ErrInvalidSizeFormat, s)
	}

	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrInvalidSizeFormat, matches[1], err)
	}

	mult, err := getMultiplier(strings.ToUpper(matches[2]))
	if err != nil {
		return 0, err
	}

	return int64(val * mult), nil
}

func getMultiplier(unit string) (float64, error) {
	switch unit {
	case "", "B":
		return 1, nil
	case "K", "KB":
		return unitKilo, nil
	case "KIB":
		return unitKibi, nil
	case "M", "MB":
		return unitMega, nil
	case "MIB":
		return unitMebi, nil
	case "G", "GB":
		return unitGiga, nil
	case "GIB":
		return unitGibi, nil
	case "T", "TB":
		return unitTera, nil
	case "TIB":
		return unitTebi, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownUnit, unit)
	}
}

// FormatBytes formats raw byte count into human-friendly string (e.g. "48.25 GiB").
func FormatBytes(bytes int64) string {
	if bytes < unitKibi {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unitKibi), 0
	for n := bytes / unitKibi; n >= unitKibi; n /= unitKibi {
		div *= unitKibi
		exp++
	}

	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
