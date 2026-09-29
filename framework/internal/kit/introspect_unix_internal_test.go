//go:build unix

package kit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A named pipe put at the name between the look and the open does not hold
// the open until a writer comes (sourceOpen's O_NONBLOCK): the handle says
// it is no regular file, at once.
//
// Goroutine lifecycle: one goroutine reads and reports on a buffered channel,
// which the test waits on within its deadline.
func TestANamedPipeSwappedInIsRefusedAtOnce(t *testing.T) {
	dir, root := sourceTree(t)
	seen, err := root.Lstat("a.go")
	must(t, err)
	must(t, makeFifo(filepath.Join(dir, "pipe"), 0o600))
	must(t, root.Rename("pipe", "a.go"))
	read := make(chan error, 1)
	go func() {
		_, err := readSeen(root, "a.go", seen)
		read <- err
	}()
	select {
	case err := <-read:
		if err == nil {
			t.Error("a named pipe was read as the source")
		}
	case <-time.After(10 * time.Second):
		// A writer lets the waiting open return, so the test ends.
		if w, err := root.OpenFile("a.go", os.O_WRONLY, 0); err == nil {
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		}
		<-read
		t.Fatal("the open waited for a writer of the named pipe")
	}
}
