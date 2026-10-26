# loki-prune

[![CI](https://github.com/BlackbirdWorks/loki-prune/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackbirdWorks/loki-prune/actions/workflows/ci.yml)
[![Release](https://github.com/BlackbirdWorks/loki-prune/actions/workflows/release.yml/badge.svg)](https://github.com/BlackbirdWorks/loki-prune/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/blackbirdworks/loki-prune)](https://goreportcard.com/report/github.com/blackbirdworks/loki-prune)

An automated storage-quota rotator and compression engine for Grafana Loki.

---

## Why loki-prune?

Grafana Loki only natively supports **time-based retention** (e.g. `retention_period: 30d`) because it was architected primarily for cloud object stores (S3/GCS). When running Loki on local disks, ZFS datasets, or persistent volume claims (PVCs), there is **no native mechanism to trigger log rotation or pruning when disk usage crosses a specific size threshold** (see [Grafana Loki Issue #6876](https://github.com/grafana/loki/issues/6876)).

`loki-prune` watches configured chunk/storage paths, safely coordinates with Loki's HTTP API (`POST /flush`), stages oldest chunks into rotation archives, and compresses them (via `zstd` or `gzip`) with configurable delay rules.

---

## Key Features

- **Multi-Path YAML Configuration:** Define individual size quotas and rotation rules for each storage path.
- **Size-Triggered Rotation:** Specify human-friendly byte thresholds (`48GB`, `10GiB`, `500MB`).
- **Loki API Coordination:**
  - Invokes `POST /flush` before rotating so all in-memory streams are committed to disk.
  - Triggers Loki synchronization after files are rotated to keep indexing consistent.
- **High-Performance Compression:**
  - Native **Zstandard (`zstd`)** and **Gzip (`gzip`)** streaming compression.
  - Configurable compression levels.
- **Flexible Delayed Compression:**
  - **By Rotation Count:** Keep the newest $N$ rotations uncompressed on disk for instant access, only compressing older ones.
  - **By Age:** Only compress rotations older than a specified duration (e.g., `24h`, `7d`).
- **Retention Management:** Automatically prunes the oldest archives when the number of rotations exceeds `max_rotations`.
- **Operational Modes:**
  - Daemon mode with periodic evaluation interval.
  - Single-shot execution (`--once`) for system cron jobs.
  - Safe dry-run mode (`--dry-run`).

---

## Installation

### Binary Releases
Download the pre-compiled binary for your architecture (Linux, macOS, Windows; amd64/arm64) from the [Releases](https://github.com/BlackbirdWorks/loki-prune/releases) page.

### Docker
```bash
docker pull ghcr.io/blackbirdworks/loki-prune:latest
```

Docker Compose:
```yaml
services:
  loki-prune:
    image: ghcr.io/blackbirdworks/loki-prune:latest
    container_name: loki-prune
    restart: unless-stopped
    volumes:
      - /share/Data/container-station-data/config/loki-prune/config.yaml:/etc/loki-prune/config.yaml:ro
      - /share/Data/metrics/loki:/data/loki
    command: ["--config", "/etc/loki-prune/config.yaml"]
```

### From Source
```bash
go install github.com/blackbirdworks/loki-prune/cmd/loki-prune@latest
```

---

## Operational Modes

`loki-prune` supports two operational modes depending on your infrastructure and monitoring requirements:

| Mode | Mechanism | Resource Profile | Best For |
| :--- | :--- | :--- | :--- |
| **`poll`** *(Default)* | Scheduled timer loop (`interval: 5m`, adaptive down to `min_interval`). Evaluates directory sizes via fast stateless directory walk on tick. | **Minimal**: Zero `inotify` watches, zero open file descriptors for directories, negligible RAM. | Production servers, QNAP/NAS, container hosts with massive chunk directories (10k+ files), or environments with constrained kernel inotify watch limits (`fs.inotify.max_user_watches`). |
| **`fsevent`** | In-memory file inventory updated in real-time via `fsnotify` with recursive directory watching. | **Low-latency**: Maintains $O(1)$ size tracking and initiates an evaluation after a configurable **cooldown** window (e.g. `5s`) once write activity quiets down. | Near-realtime rotation needs where rapid log spikes must be pruned immediately without waiting for a polling tick. |

Set the mode via YAML config (`daemon.mode: "poll"` or `"fsevent"`) or override via `--mode <poll|fsevent>`.

---

## Configuration

Example `loki-prune.yaml`:

```yaml
# Loki API settings
loki:
  url: "http://127.0.0.1:3100"
  timeout: "30s"
  flush_before_rotate: true   # Calls POST /flush to push memory chunks to disk before rotating
  reload_after_rotate: true   # Synchronizes Loki storage post-rotation

# Daemon settings
daemon:
  mode: "poll"                # "poll" (default) or "fsevent"
  interval: "5m"              # Evaluation frequency in poll mode; heartbeat in fsevent mode
  cooldown: "5s"              # Event cooldown before evaluating (used in fsevent mode)
  adaptive: true              # Automatically scales sleep interval based on quota pressure
  min_interval: "5s"          # Minimum sleep interval when quota ratio >= 1.0
  log_level: "info"           # debug, info, warn, error
  log_type: "text"            # "text" or "json"

# Storage paths to manage
paths:
  - name: "loki-chunks"
    path: "/data/loki/chunks"
    archive_dir: "/data/loki/archive"
    disk_guard_percent: 90.0   # Emergency trigger if host disk exceeds 90% full

    # Thresholds
    trigger_size: "48GB"       # Begin rotation when directory exceeds 48 GB
    target_size: "40GB"        # Move chunks until directory reaches 40 GB

    rotation:
      # Keep up to 10 rotated archives (oldest are purged)
      max_rotations: 10

      # Compression
      compress: true
      type: "zstd"             # "zstd", "gzip", or "none"
      level: 3                 # 1-19 for zstd, 1-9 for gzip

      # Delayed compression
      delay:
        rotations: 2           # Keep the 2 newest rotations uncompressed
        age: "24h"             # Or only compress after 24 hours
```

---

## Usage

```bash
# Run as a continuous daemon (default: poll mode)
loki-prune --config /path/to/config.yaml

# Run with real-time filesystem event detection (with cooldown)
loki-prune --config /path/to/config.yaml --mode fsevent

# Run once and exit (ideal for crontab)
loki-prune --config /path/to/config.yaml --once

# Dry-run evaluation (no files moved or deleted)
loki-prune --config /path/to/config.yaml --dry-run
```

---

## License

[MIT](LICENSE)
