// Package service implements the stateful daemon service, file tracking, debouncing,
// and lifecycle orchestration for loki-prune.
package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blackbirdworks/loki-prune/pkg/rotator"
)

// ErrTrackerClosed is returned when operating on a closed tracker.
var ErrTrackerClosed = errors.New("tracker is closed")

const defaultDebounceDuration = 1 * time.Second

// Tracker maintains an in-memory inventory of monitored files and signals on debounced filesystem changes.
type Tracker struct {
	watcher        *fsnotify.Watcher
	debounceTimer  *time.Timer
	items          map[string]rotator.FileItem
	pendingUpdates map[string]struct{}
	events         chan struct{}
	stopCh         chan struct{}
	rootPath       string
	archiveDir     string
	archivePrefix  string
	debounceDelay  time.Duration
	totalSize      int64
	mu             sync.RWMutex
	closeOnce      sync.Once
	closed         bool
}

// NewTracker creates a new Tracker for rootPath, ignoring paths under archiveDir.
func NewTracker(rootPath, archiveDir string, debounceDelay time.Duration) (*Tracker, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	cleanRoot := filepath.Clean(rootPath)
	cleanArchive := filepath.Clean(archiveDir)

	if debounceDelay <= 0 {
		debounceDelay = defaultDebounceDuration
	}

	trk := &Tracker{
		watcher:        watcher,
		items:          make(map[string]rotator.FileItem),
		pendingUpdates: make(map[string]struct{}),
		events:         make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
		rootPath:       cleanRoot,
		archiveDir:     cleanArchive,
		archivePrefix:  cleanArchive + string(filepath.Separator),
		debounceDelay:  debounceDelay,
	}

	if _, statErr := os.Stat(cleanRoot); statErr == nil {
		_ = watcher.Add(cleanRoot)
	}

	return trk, nil
}

// Start begins the event processing loop in a background goroutine.
func (t *Tracker) Start(ctx context.Context) {
	go t.eventLoop(ctx)
}

func (t *Tracker) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			t.handleFSEvent(event)
		case _, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (t *Tracker) isArchive(path string) bool {
	return path == t.archiveDir || strings.HasPrefix(path, t.archivePrefix)
}

func (t *Tracker) handleFSEvent(event fsnotify.Event) {
	if t.isArchive(event.Name) {
		return
	}

	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		st, err := os.Stat(event.Name)
		if err == nil && st.IsDir() {
			t.watchRecursively(event.Name)

			return
		}

		t.recordPendingUpdate(event.Name)
		t.triggerDebounce()

	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		t.removeFSEventItem(event.Name)
		t.triggerDebounce()
	}
}

func (t *Tracker) recordPendingUpdate(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pendingUpdates[path] = struct{}{}
}

func (t *Tracker) removeFSEventItem(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if old, exists := t.items[path]; exists {
		t.totalSize -= old.Size
		delete(t.items, path)
	}
	delete(t.pendingUpdates, path)
}

// watchRecursively registers a directory and all its subdirectories with the watcher.
func (t *Tracker) watchRecursively(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if t.isArchive(path) {
				return filepath.SkipDir
			}
			_ = t.watcher.Add(path)
		}

		return nil
	})
}

func (t *Tracker) triggerDebounce() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	if t.debounceTimer == nil {
		t.debounceTimer = time.AfterFunc(t.debounceDelay, t.onDebounceTimer)
	} else {
		t.debounceTimer.Reset(t.debounceDelay)
	}
}

func (t *Tracker) onDebounceTimer() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	for path := range t.pendingUpdates {
		st, err := os.Stat(path)
		if err != nil {
			if old, exists := t.items[path]; exists {
				t.totalSize -= old.Size
				delete(t.items, path)
			}

			continue
		}
		if st.IsDir() {
			continue
		}

		old, exists := t.items[path]
		if exists {
			t.totalSize += st.Size() - old.Size
		} else {
			t.totalSize += st.Size()
		}
		t.items[path] = rotator.FileItem{
			Path:    path,
			Size:    st.Size(),
			ModTime: st.ModTime(),
		}
	}
	clear(t.pendingUpdates)

	select {
	case t.events <- struct{}{}:
	default:
	}
}

// Sync scans the filesystem to populate the in-memory cache and registers watches on directories recursively.
func (t *Tracker) Sync() (int64, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return 0, 0, ErrTrackerClosed
	}

	st, err := os.Stat(t.rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.items = make(map[string]rotator.FileItem)
			t.totalSize = 0

			return 0, 0, nil
		}

		return 0, 0, fmt.Errorf("stat %s: %w", t.rootPath, err)
	}

	if !st.IsDir() {
		t.items = map[string]rotator.FileItem{
			t.rootPath: {
				Path:    t.rootPath,
				Size:    st.Size(),
				ModTime: st.ModTime(),
			},
		}
		t.totalSize = st.Size()

		return t.totalSize, 1, nil
	}

	newItems := make(map[string]rotator.FileItem)
	var newTotal int64

	walkErr := filepath.WalkDir(t.rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if t.isArchive(path) {
				return filepath.SkipDir
			}
			_ = t.watcher.Add(path)

			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}

		newItems[path] = rotator.FileItem{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		newTotal += info.Size()

		return nil
	})

	if walkErr != nil {
		return 0, 0, fmt.Errorf("walking %s: %w", t.rootPath, walkErr)
	}

	t.items = newItems
	t.totalSize = newTotal

	return t.totalSize, len(t.items), nil
}

// Size returns the cached total byte size and file count in O(1) time without disk I/O.
func (t *Tracker) Size() (int64, int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.totalSize, len(t.items)
}

// OldestItems returns a slice of cached entries sorted from oldest to newest by ModTime.
func (t *Tracker) OldestItems() []rotator.FileItem {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]rotator.FileItem, 0, len(t.items))
	for _, entry := range t.items {
		entries = append(entries, entry)
	}

	slices.SortFunc(entries, func(a, b rotator.FileItem) int {
		return a.ModTime.Compare(b.ModTime)
	})

	return entries
}

// Remove deletes a file from the in-memory cache and updates totalSize.
func (t *Tracker) Remove(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if entry, ok := t.items[path]; ok {
		t.totalSize -= entry.Size
		delete(t.items, path)
	}
}

// Events returns the channel signaling when debounced filesystem changes occurred.
func (t *Tracker) Events() <-chan struct{} {
	return t.events
}

// Close terminates the filesystem watcher and cleans up resources.
func (t *Tracker) Close() error {
	var err error
	t.closeOnce.Do(func() {
		func() {
			t.mu.Lock()
			defer t.mu.Unlock()

			t.closed = true
			if t.debounceTimer != nil {
				t.debounceTimer.Stop()
			}
			close(t.events)
		}()

		close(t.stopCh)
		err = t.watcher.Close()
	})

	return err
}
