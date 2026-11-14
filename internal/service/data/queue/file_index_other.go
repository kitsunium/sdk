//go:build !linux

package queue

import (
	"errors"
	"io"
)

// direntBatch is how many names one read of a state directory asks for: a
// directory of a million names is read in steps, never in one slice.
const direntBatch int = 256

// stateWatch is the change queue a platform without inotify does not have:
// its directories are read under the listings' rule (file_index.go). macOS's
// FSEvents and kqueue, and the BSDs' and illumos' interfaces, say that a
// directory changed and not what, which cannot tell this broker's renames
// from another process's; Windows' ReadDirectoryChangesW would, and is not
// wired yet.
type stateWatch struct{}

// watchStates watches nothing here, and says why.
func watchStates(_ dirOpener) (watch *stateWatch, why string) {
	//: read under the listings' rule.
	return nil, "this platform names no directory change by its entry: inotify is Linux's"
}

// drain is never reached: there is no watch to drain.
func (*stateWatch) drain(_ *fileIndex) (lost bool, err error) {
	//: nothing queued.
	return false, nil
}

// close is never reached: there is no watch to release.
func (*stateWatch) close() error {
	//: nothing held.
	return nil
}

// readdirnameser reads a directory's names in steps: an opened *os.File.
type readdirnameser interface {
	Readdirnames(n int) (names []string, err error)
}

// readEntries reads dir whole into index, direntBatch names a step. The
// platform's reader copies every name out, so a read allocates one string an
// entry; nothing is sorted.
func readEntries(dir readdirnameser, _ []byte, index *dirIndex) error {
	//: until the end of the directory.
	for {
		names, readErr := dir.Readdirnames(direntBatch)
		//: each name, indexed or kept.
		for _, name := range names {
			index.see(name)
		}
		//: the end of the directory.
		if errors.Is(readErr, io.EOF) {
			//: read whole.
			return nil
		}
		//: the medium refused.
		if readErr != nil {
			//: QueueBackendFailed, by the caller.
			return readErr
		}
	}
}
