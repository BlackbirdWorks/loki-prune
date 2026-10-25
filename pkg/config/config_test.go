package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseBytes(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasErr   bool
	}{
		{"100B", 100, false},
		{"10KB", 10000, false},
		{"10KiB", 10240, false},
		{"500MB", 500000000, false},
		{"500MiB", 500 * 1024 * 1024, false},
		{"48GB", 48 * 1000 * 1000 * 1000, false},
		{"48GiB", 48 * 1024 * 1024 * 1024, false},
		{"1TB", 1000000000000, false},
		{"1TiB", 1024 * 1024 * 1024 * 1024, false},
		{"invalid", 0, true},
		{"100XB", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseBytes(tt.input)
		if tt.hasErr && err == nil {
			t.Errorf("ParseBytes(%q) expected error, got %d", tt.input, got)
		}
		if !tt.hasErr && err != nil {
			t.Errorf("ParseBytes(%q) unexpected error: %v", tt.input, err)
		}
		if !tt.hasErr && got != tt.expected {
			t.Errorf("ParseBytes(%q) = %d, expected %d", tt.input, got, tt.expected)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	yamlContent := `
loki:
  url: "http://localhost:3100"
  timeout: "15s"
  flush_before_rotate: true
  reload_after_rotate: true

daemon:
  interval: "2m"
  log_level: "debug"

paths:
  - name: "test-chunks"
    path: "/tmp/loki/chunks"
    archive_dir: "/tmp/loki/archive"
    trigger_size: "100MB"
    target_size: "80MB"
    rotation:
      max_rotations: 5
      compress: true
      type: "zstd"
      level: 3
      delay:
        rotations: 2
        age: "12h"
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Loki.URL != "http://localhost:3100" {
		t.Errorf("expected loki url http://localhost:3100, got %s", cfg.Loki.URL)
	}
	if cfg.Loki.Timeout != 15*time.Second {
		t.Errorf("expected loki timeout 15s, got %v", cfg.Loki.Timeout)
	}
	if cfg.Daemon.Interval != 2*time.Minute {
		t.Errorf("expected interval 2m, got %v", cfg.Daemon.Interval)
	}
	if len(cfg.Paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(cfg.Paths))
	}

	p := cfg.Paths[0]
	if p.TriggerSizeBytes != 100*1000*1000 {
		t.Errorf("expected trigger 100MB (%d), got %d", 100*1000*1000, p.TriggerSizeBytes)
	}
	if p.TargetSizeBytes != 80*1000*1000 {
		t.Errorf("expected target 80MB (%d), got %d", 80*1000*1000, p.TargetSizeBytes)
	}
	if p.Rotation.MaxRotations != 5 {
		t.Errorf("expected max_rotations 5, got %d", p.Rotation.MaxRotations)
	}
	if p.Rotation.Delay.Rotations != 2 {
		t.Errorf("expected delay rotations 2, got %d", p.Rotation.Delay.Rotations)
	}
	if p.Rotation.Delay.Age != 12*time.Hour {
		t.Errorf("expected delay age 12h, got %v", p.Rotation.Delay.Age)
	}
}
