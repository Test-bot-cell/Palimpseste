package editserver

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// event is one typed SSE message (§8: builds, errors, reloads — media progress
// joins at M3). Name maps to the SSE `event:` field, Data to `data:`.
type event struct {
	Name string
	Data string
}

// hub is a tiny fan-out for editor events: every connected browser holds one
// SSE subscription, and a filesystem change or a save-triggered build pings all
// of them at once. Sends are non-blocking, so one stalled client never wedges
// the broadcaster.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[chan event]struct{})}
}

// subscribe registers a new listener and returns its channel. The buffer lets a
// burst land even if the reader is momentarily busy; deeper backlogs are
// dropped because the browser only acts on the latest state anyway.
func (h *hub) subscribe() chan event {
	ch := make(chan event, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// unsubscribe removes and closes a listener's channel. It is safe to call once.
func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// broadcast sends ev to every subscriber without blocking on any single one.
func (h *hub) broadcast(ev event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // subscriber lags; dropping is safe, state is re-read on reload
		}
	}
}

// debounce coalesces the burst of events a single save emits (an editor writing
// a scratch file then renaming it over the target) into one reload.
const debounce = 150 * time.Millisecond

// watcher wraps fsnotify to report changes under a set of roots. fsnotify is
// event-driven — no polling, so the binary is genuinely idle at rest (§15) — but
// non-recursive, so the watcher walks each root and registers every directory,
// adding newly created ones as they appear. Hidden entries are ignored: this
// skips both the .git churn a commit produces and the ".pal-*.tmp" scratch files
// atomicWrite renames through, so neither ever triggers a spurious reload.
type watcher struct {
	fsw   *fsnotify.Watcher
	roots []string
}

func newWatcher(roots ...string) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &watcher{fsw: fsw, roots: roots}
	for _, root := range roots {
		if err := w.addTree(root); err != nil {
			_ = fsw.Close()
			return nil, err
		}
	}
	return w, nil
}

// Close releases the underlying fsnotify watcher.
func (w *watcher) Close() error { return w.fsw.Close() }

// addTree registers dir and all of its non-hidden subdirectories. A missing dir
// is skipped silently — it will be picked up via its parent once created.
func (w *watcher) addTree(dir string) error {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if isHidden(d.Name()) && p != dir {
			return filepath.SkipDir
		}
		return w.fsw.Add(p)
	})
}

// Watch reads filesystem events until ctx is done, invoking onChange — coalesced
// by a short debounce — whenever a non-hidden file changes. Newly created
// directories are registered so edits beneath them are seen too.
func (w *watcher) Watch(ctx context.Context, onChange func()) {
	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
		}
	}
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if isHidden(filepath.Base(ev.Name)) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = w.addTree(ev.Name)
				}
			}
			if timer == nil {
				timer = time.AfterFunc(debounce, onChange)
			} else {
				timer.Reset(debounce)
			}
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// isHidden reports whether a path element is a dotfile (excluding the "." and
// ".." specials that WalkDir never yields as names anyway).
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}
