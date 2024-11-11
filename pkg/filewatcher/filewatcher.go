package filewatcher

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog/v2"
)

var watchCertificateFileOnce sync.Once

// WatchFileForChanges watches the file, fileToWatch, for changes. If the file contents have changed, the pod this
// function is running on will be restarted.
func WatchFileForChanges(fileToWatch string) error {
	var err error

	// This starts only one occurrence of the file watcher, which watches the file, fileToWatch.
	watchCertificateFileOnce.Do(func() {
		klog.Infof("Starting the file change watcher on file, %s", fileToWatch)

		// Update the file path to watch in case this is a symlink
		fileToWatch, err = filepath.EvalSymlinks(fileToWatch)
		if err != nil {
			return
		}
		klog.Infof("Watching file, %s", fileToWatch)

		// Start the file watcher to monitor file changes
		go func() {
			err = checkForFileChanges(fileToWatch)
		}()
	})
	return err
}

// checkForFileChanges starts a new file watcher. If the file is changed, the pod running this function will exit.
func checkForFileChanges(path string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if ok && (event.Has(fsnotify.Write) || event.Has(fsnotify.Chmod) || event.Has(fsnotify.Remove)) {
					klog.Infof("file, %s, was modified, exiting...", event.Name)
					os.Exit(0)
				}
			case err, ok := <-watcher.Errors:
				if ok {
					klog.Errorf("file watcher error: %v", err)
				}
			}
		}
	}()

	err = watcher.Add(path)
	if err != nil {
		return err
	}

	return nil
}
