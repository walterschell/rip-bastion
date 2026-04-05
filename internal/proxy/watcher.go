package proxy

import (
	"log"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchConfig starts a fsnotify watcher on the file at path. When the file is
// written or recreated (common with editors that do atomic saves), onChange is
// called after a short debounce delay so that rapid bursts of events are
// collapsed into a single reload.
//
// The returned stop function must be called to release resources.
func watchConfig(path string, onChange func()) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch the directory that contains the file so that we catch atomic
	// rename-style saves (where the original inode is replaced).
	if err := watcher.Add(path); err != nil {
		_ = watcher.Close()
		return nil, err
	}

	go func() {
		// debounce: collapse multiple rapid events into one reload.
		var debounce <-chan time.Time
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Name != path {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					debounce = time.After(150 * time.Millisecond)
				}
			case <-debounce:
				debounce = nil
				onChange()
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("proxy: config watcher error: %v", watchErr)
			}
		}
	}()

	return func() { _ = watcher.Close() }, nil
}
