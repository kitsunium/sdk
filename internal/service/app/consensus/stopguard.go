package consensus

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// unmarked holds, by absolute path, every data directory a node of this
// process stopped on with STORAGE_FAILED and no marker written in either
// place. A consumer that does not exit on that stop would otherwise open it
// again in the same boot and trust the page cache the failed fdatasync left
// (R9-05, #562).
var unmarked sync.Map

// noteUnmarked records dir in unmarked.
func noteUnmarked(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	unmarked.Store(dir, struct{}{})
}

// refuseUnmarked refuses a data directory a node of this process stopped on
// with no marker: the process must exit, and the directory waits for a
// reboot.
func refuseUnmarked(dir string) error {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if _, ok := unmarked.Load(dir); ok {
		return origin(coreconsensus.StopMarkerPresent, errs.String("dir", dir),
			errs.String("detail", "a STORAGE_FAILED stop of this process wrote no marker: exit, and start again after a reboot"))
	}
	return nil
}

// probeRuntimeDir creates the boot-scoped directory and writes, then
// removes, a file there. A directory the process cannot write in left every
// STORAGE_FAILED stop with one marker of two, and with none when the data
// directory's failed too: the start is refused while nothing has been
// written (R9-05, #562).
func (l *fileLog) probeRuntimeDir() error {
	probe := l.runtimeMarker("PROBE")
	err := os.MkdirAll(l.runDir, 0o700)
	if err == nil {
		err = writeFileSync(probe, nil)
	}
	if err == nil {
		err = os.Remove(probe)
	}
	if err != nil {
		return misconfigured("RuntimeDir", fmt.Sprintf("the boot-scoped marker directory %s is not writable: %v", l.runDir, err))
	}
	return nil
}

// storageFailedThisBoot is the STOP_MARKER_PRESENT of a STORAGE_FAILED
// marker of the current boot, in either place, nil when none applies. In
// that boot the page cache may still show bytes whose fdatasync failed:
// nothing writes to the log, the snapshots or the stamp until a reboot, or
// a recovery, a Recover or an Init writes durable data after pages the disk
// does not hold, and the reboot that clears the marker quarantines the log
// (ADR 0152 "fail closed before any mutation", #563).
func (l *fileLog) storageFailedThisBoot() error {
	return l.markerApplies(true)
}

// markerApplies is checkMarkers' reading of the markers, limited to the
// STORAGE_FAILED ones when storageOnly is set.
func (l *fileLog) markerApplies(storageOnly bool) error {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return storageFailed("markers", err)
	}
	boot := []byte("boot=" + bootID() + "\n")
	for _, p := range l.markerPaths(ents) {
		if storageOnly && !strings.HasSuffix(p, "STORAGE_FAILED") {
			continue
		}
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			// No marker there: the boot-scoped one is absent on every start
			// that followed no storage failure.
			continue
		}
		if err != nil {
			// A marker that is there but cannot be read is a stop whose
			// reason is unknown, never an absent one: the start is refused.
			return storageFailed("read stop marker", err)
		}
		if strings.HasSuffix(p, "STORAGE_FAILED") && !bytes.HasPrefix(b, boot) {
			// Another boot: the page cache that may have lied is gone.
			continue
		}
		return stopMarkerPresent(p, b)
	}
	return nil
}
