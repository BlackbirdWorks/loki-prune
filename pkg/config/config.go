package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the top-level loki-prune configuration.
type Config struct {
	Loki   LokiConfig   `yaml:"loki"`
	Daemon DaemonConfig `yaml:"daemon"`
	Paths  []PathConfig `yaml:"paths"`
}

// LokiConfig holds settings for communicating with the Loki API.
type LokiConfig struct {
	URL               string        `yaml:"url"`
	TimeoutStr        string        `yaml:"timeout"`
	Timeout           time.Duration `yaml:"-"`
	FlushBeforeRotate bool          `yaml:"flush_before_rotate"`
	ReloadAfterRotate bool          `yaml:"reload_after_rotate"`
}

// DaemonConfig holds service/scheduling options.
type DaemonConfig struct {
	IntervalStr string        `yaml:"interval"`
	Interval    time.Duration `yaml:"-"`
	LogLevel    string        `yaml:"log_level"`
}

// PathConfig defines rotation rules for a specific directory.
type PathConfig struct {
	Name            string         `yaml:"name"`
	Path            string         `yaml:"path"`
	ArchiveDir      string         `yaml:"archive_dir"`
	TriggerSizeStr  string         `yaml:"trigger_size"`
	TriggerSizeBytes int64         `yaml:"-"`
	TargetSizeStr   string         `yaml:"target_size"`
	TargetSizeBytes int64          `yaml:"-"`
	Rotation        RotationConfig `yaml:"rotation"`
}

// RotationConfig defines compression and retention policies for rotated items.
type RotationConfig struct {
	MaxRotations int           `yaml:"max_rotations"`
	Compress     bool          `yaml:"compress"`
	Type         string        `yaml:"type"`  // "zstd" or "gzip"
	Level        int           `yaml:"level"` // 1-9 for gzip, 1-19 for zstd (or 0 for default)
	Delay        DelayConfig   `yaml:"delay"`
}

// DelayConfig controls when compression takes place.
type DelayConfig struct {
	Rotations int           `yaml:"rotations"` // Keep newest N rotations uncompressed
	AgeStr    string        `yaml:"age"`       // Keep uncompressed until older than age (e.g. "24h")
	Age       time.Duration `yaml:"-"`
}

// Load reads and parses a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}

	if err := cfg.validateAndSetDefaults(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validateAndSetDefaults(baseDir string) error {
	// Loki defaults
	if c.Loki.URL == "" {
		c.Loki.URL = "http://127.0.0.1:3100"
	}
	c.Loki.URL = strings.TrimRight(c.Loki.URL, "/")

	if c.Loki.TimeoutStr == "" {
		c.Loki.Timeout = 30 * time.Second
	} else {
		d, err := time.ParseDuration(c.Loki.TimeoutStr)
		if err != nil {
			return fmt.Errorf("invalid loki.timeout '%s': %w", c.Loki.TimeoutStr, err)
		}
		c.Loki.Timeout = d
	}

	// Daemon defaults
	if c.Daemon.IntervalStr == "" {
		c.Daemon.Interval = 5 * time.Minute
	} else {
		d, err := time.ParseDuration(c.Daemon.IntervalStr)
		if err != nil {
			return fmt.Errorf("invalid daemon.interval '%s': %w", c.Daemon.IntervalStr, err)
		}
		c.Daemon.Interval = d
	}

	if c.Daemon.LogLevel == "" {
		c.Daemon.LogLevel = "info"
	}

	if len(c.Paths) == 0 {
		return fmt.Errorf("no paths defined in config")
	}

	for i := range c.Paths {
		p := &c.Paths[i]
		if p.Name == "" {
			p.Name = fmt.Sprintf("path-%d", i+1)
		}
		if p.Path == "" {
			return fmt.Errorf("path[%d] (%s) missing required 'path'", i, p.Name)
		}

		// Parse Trigger Size
		if p.TriggerSizeStr == "" {
			return fmt.Errorf("path[%d] (%s) missing required 'trigger_size'", i, p.Name)
		}
		bytes, err := ParseBytes(p.TriggerSizeStr)
		if err != nil {
			return fmt.Errorf("path[%d] (%s) invalid trigger_size '%s': %w", i, p.Name, p.TriggerSizeStr, err)
		}
		p.TriggerSizeBytes = bytes

		// Parse Target Size
		if p.TargetSizeStr != "" {
			tBytes, err := ParseBytes(p.TargetSizeStr)
			if err != nil {
				return fmt.Errorf("path[%d] (%s) invalid target_size '%s': %w", i, p.Name, p.TargetSizeStr, err)
			}
			p.TargetSizeBytes = tBytes
		} else {
			// Default to 80% of trigger size
			p.TargetSizeBytes = int64(float64(p.TriggerSizeBytes) * 0.8)
		}

		if p.TargetSizeBytes >= p.TriggerSizeBytes {
			return fmt.Errorf("path[%d] (%s) target_size must be less than trigger_size", i, p.Name)
		}

		// Archive directory defaults to <path>/archive if not specified
		if p.ArchiveDir == "" {
			p.ArchiveDir = filepath.Join(p.Path, "archive")
		}

		// Compression defaults
		if p.Rotation.Type == "" {
			p.Rotation.Type = "zstd"
		}
		p.Rotation.Type = strings.ToLower(p.Rotation.Type)
		if p.Rotation.Type != "zstd" && p.Rotation.Type != "gzip" && p.Rotation.Type != "none" {
			return fmt.Errorf("path[%d] (%s) unsupported compression type '%s' (use 'zstd', 'gzip', or 'none')", i, p.Name, p.Rotation.Type)
		}

		if p.Rotation.Delay.AgeStr != "" {
			age, err := time.ParseDuration(p.Rotation.Delay.AgeStr)
			if err != nil {
				return fmt.Errorf("path[%d] (%s) invalid delay.age '%s': %w", i, p.Name, p.Rotation.Delay.AgeStr, err)
			}
			p.Rotation.Delay.Age = age
		}
	}

	return nil
}

var sizeRegex = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-z]*)\s*$`)

// ParseBytes converts human-friendly byte strings (e.g. "48GB", "500MB", "10GiB") into raw bytes.
func ParseBytes(s string) (int64, error) {
	matches := sizeRegex.FindStringSubmatch(strings.TrimSpace(s))
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid size format: %q", s)
	}

	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", matches[1], err)
	}

	unit := strings.ToUpper(matches[2])
	var multiplier float64

	switch unit {
	case "", "B":
		multiplier = 1
	case "K", "KB":
		multiplier = 1000
	case "KIB":
		multiplier = 1024
	case "M", "MB":
		multiplier = 1000 * 1000
	case "MIB":
		multiplier = 1024 * 1024
	case "G", "GB":
		multiplier = 1000 * 1000 * 1000
	case "GIB":
		multiplier = 1024 * 1024 * 1024
	case "T", "TB":
		multiplier = 1000 * 1000 * 1000 * 1000
	case "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit %q", matches[2])
	}

	return int64(val * multiplier), nil
}

// FormatBytes formats raw byte count into human-friendly string (e.g. "48.25 GB").
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
