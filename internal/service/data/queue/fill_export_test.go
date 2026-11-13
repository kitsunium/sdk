// Package queue — hands the scale benchmark a backlog it can afford: a
// million queued messages written straight into ready/, by the name grammar,
// without the two flushes a Publish makes (issue #811).
package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FillReadyForTest writes count queued messages of size bytes each into
// the ready/ directory of the file broker over dir, which NewFile has
// prepared: visible since at, enqueued one nanosecond apart in the order they
// are written, each entropy the message's index — names the grammar accepts
// and a broker cannot tell from its own. Nothing is flushed: Publish's two
// fsyncs would take an hour over a million messages, and the benchmark
// measures the Receive, not the device.
func FillReadyForTest(dir string, count, size int, at time.Time) error {
	payload := make([]byte, size)
	ready := filepath.Join(dir, dirReady)
	for i := range count {
		name := nameValue{
			At: at.UnixNano(), EnqueuedAt: at.UnixNano() + int64(i),
			Entropy: fmt.Sprintf("%0*x", 2*idEntropyBytes, i),
		}
		//: one file a message, its payload the bytes Receive reads back.
		if err := os.WriteFile(filepath.Join(ready, name.readyName()), payload, messageMode); err != nil {
			return err
		}
	}
	return nil
}
