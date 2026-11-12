package consensus

import (
	"os"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
)

// preallocEverywhere opens every log in PreallocateFallocate, whatever its
// options say. Only a test binary sets it, run with
// CONSENSUS_PREALLOCATE=fallocate, so the whole suite runs on fallocated
// segments (#922); it is read by openFileLog alone.
var preallocEverywhere bool

// probePreallocate answers why fallocate(2) cannot reserve a segment in
// dir, empty when it can: fallocateProbe, unless a test replaces it to
// answer as a filesystem without fallocate does. Only an open's own
// goroutine reads it.
var probePreallocate = fallocateProbe

// resolvePreallocate sets how the log preallocates its segments: the mode
// asked for, or PreallocateZeroFill when fallocate is asked for and the
// filesystem or the platform answers it cannot. That is never a refusal:
// the reason is kept for the one line bindWarn logs (#922). It runs before
// recovery prepares the next segment.
func (l *fileLog) resolvePreallocate(mode Preallocate) {
	if preallocEverywhere {
		mode = PreallocateFallocate
	}
	l.prealloc = PreallocateZeroFill
	if mode != PreallocateFallocate {
		return
	}
	if why := probePreallocate(l.logDir()); why != "" {
		l.preallocWhy = why
		return
	}
	l.prealloc = PreallocateFallocate
}

// bindWarn implements bindWarner: a log that asked for fallocate and
// zero-fills, or for RWF_DSYNC and syncs with fdatasync (#923), says so
// once, one line each, through the log of the first node it serves.
func (l *fileLog) bindWarn(warn func(msg string, attrs ...corelogger.AttrValue)) {
	l.mu.Lock()
	why, syncWhy := l.preallocWhy, l.syncWhy
	l.preallocWhy, l.syncWhy = "", ""
	l.mu.Unlock()
	if why != "" {
		warn("consensus: the file log zero-fills its segments, fallocate is not available",
			lstr("dir", l.dir), lstr("why", why))
	}
	if syncWhy != "" {
		warn("consensus: the file log syncs with fdatasync, RWF_DSYNC is not available",
			lstr("dir", l.dir), lstr("why", syncWhy))
	}
}

// fallocateFill is zeroFrom's counterpart in PreallocateFallocate: f's
// range from off to size is reserved and reads as zeros, then synced. A
// filesystem that stops answering to fallocate after the open's probe gets
// the zeros written instead.
func fallocateFill(f *os.File, off, size int64) error {
	ok, err := fallocateRange(f, off, size)
	if err != nil {
		return err
	}
	if !ok {
		return zeroFrom(f, off, size)
	}
	return datasync(f)
}
