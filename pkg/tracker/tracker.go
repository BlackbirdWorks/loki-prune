// Package tracker provides real-time in-memory file tracking and filesystem event notifications.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrTrackerClosed is returned when operating on a closed tracker.
var ErrTrackerClosed = errors.New("tracker is closed")

// Entry holds cached metadata for a tracked file.
type Entry struct {
	ModTime time.Time
	Path    string
	Size    int64
}

// Tracker maintains an in-memory inventory of monitored files and signals on filesystem changes.
type Tracker struct {
	watcher    *fsnotify.Watcher
	items      map[string]Entry
	events     chan struct{}
	stopCh     chan struct{}
	rootPath   string
	archiveDir string
	totalSize  int64
	mu         sync.RWMutex
	closeOnce  sync.Once
	closed     bool
}

// New creates a new Tracker for rootPath, ignoring paths under archiveDir.
func New(rootPath, archiveDir string) (*Tracker, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	cleanRoot := filepath.Clean(rootPath)
	cleanArchive := filepath.Clean(archiveDir)

	trk := &Tracker{
		watcher:    watcher,
		items:      make(map[string]Entry),
		events:     make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		rootPath:   cleanRoot,
		archiveDir: cleanArchive,
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
	clean := filepath.Clean(path)
	if clean == t.archiveDir {
		return true
	}

	return strings.HasPrefix(clean, t.archiveDir+string(filepath.Separator))
}

func (t *Tracker) handleFSEvent(event fsnotify.Event) {
	if t.isArchive(event.Name) {
		return
	}

	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		st, err := os.Stat(event.Name)
		if err != nil {
			return
		}
		if st.IsDir() {
			_ = t.watcher.Add(event.Name)

			return
		}

		t.mu.Lock()
		old, exists := t.items[event.Name]
		if exists {
			t.totalSize += st.Size() - old.Size
		} else {
			t.totalSize += st.Size()
		}
		t.items[event.Name] = Entry{
			Path:    event.Name,
			Size:    st.Size(),
			ModTime: st.ModTime(),
		}
		t.mu.Unlock()
		t.notify()

	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		t.mu.Lock()
		if old, exists := t.items[event.Name]; exists {
			t.totalSize -= old.Size
			delete(t.items, event.Name)
			t.mu.Unlock()
			t.notify()
		} else {
			t.mu.Unlock()
		}
	}
}

func (t *Tracker) notify() {
	select {
	case t.events <- struct{}{}:
	default:
	}
}

// Sync scans the filesystem to populate the in-memory cache and registers watches on directories.
func (t *Tracker) Sync() (int64, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return 0, 0, ErrTrackerClosed
	}

	st, err := os.Stat(t.rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.items = make(map[string]Entry)
			t.totalSize = 0

			return 0, 0, nil
		}

		return 0, 0, fmt.Errorf("stat %s: %w", t.rootPath, err)
	}

	if !st.IsDir() {
		t.items = map[string]Entry{
			t.rootPath: {
				Path:    t.rootPath,
				Size:    st.Size(),
				ModTime: st.ModTime(),
			},
		}
		t.totalSize = st.Size()

		return t.totalSize, 1, nil
	}

	newItems := make(map[string]Entry)
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

		newItems[path] = Entry{
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
func (t *Tracker) OldestItems() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]Entry, 0, len(t.items))
	for _, entry := range t.items {
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ModTime.Before(entries[j].ModTime)
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

// Events returns the channel signaling when filesystem changes occurred.
func (t *Tracker) Events() <-chan struct{} {
	return t.events
}

// Close terminates the filesystem watcher and cleans up resources.
func (t *Tracker) Close() error {
	var err error
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()

		close(t.stopCh)
		err = t.watcher.Close()
	})

	return err
}
