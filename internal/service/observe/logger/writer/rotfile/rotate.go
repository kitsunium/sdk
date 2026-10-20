package rotfile

import (
	"os"

	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maybeRotate rotates the active file when appending incoming bytes would push
// it past cfg.MaxBytes. It is a no-op when rotation is disabled (MaxBytes <= 0),
// when the file is still empty (a single record larger than the cap is written
// rather than spun into an empty backup), or when the threshold is not reached.
// The caller holds s.mu.
func (s *rotatingSink) maybeRotate(incoming int64) error {
	//: skip rotation when it is disabled (MaxBytes <= 0), when the file is
	//: still empty (a single oversized record lands rather than spinning an
	//: empty backup), or when the projected size stays within the cap.
	if s.cfg.MaxBytes <= 0 || s.size == 0 || s.size+incoming <= s.cfg.MaxBytes {
		//: nothing to rotate — keep the current descriptor.
		return nil
	}
	//: threshold tripped — perform the close/shift/reopen cycle.
	return s.rotate()
}

// rotate closes the active file, shifts the .N backups, optionally gzips the
// freshly rotated Path.1, then reopens Path through the hardened path so the
// symlink + O_NOFOLLOW + 0600 checks re-run. The caller holds s.mu.
func (s *rotatingSink) rotate() error {
	//: a sink whose last reopen failed holds no descriptor: open one first, so
	//: the cut below has a file to close and rename.
	if oerr := s.ensureOpen(); oerr != nil {
		//: still refused — RotFileOpenFailed, nothing rotated.
		return oerr
	}
	//: close the active descriptor before renaming it.
	if cerr := s.f.Close(); cerr != nil {
		//: surface the close failure under the rotate sentinel.
		return wrapRotate(cerr, "service/observe/logger/writer/rotfile.rotate: closing active file")
	}
	//: shift Path.N-1 -> Path.N … and rename Path -> Path.1 (+ optional gzip).
	if serr := s.shiftBackups(); serr != nil {
		//: V45: the active fd is already closed; without a reopen s.f stays a dead
		//: descriptor and every later Write re-enters rotate() -> Close-on-closed ->
		//: bricked sink. Reopen Path + re-seed s.size so the sink self-heals on the
		//: next Write while still surfacing this transient shift failure.
		s.reopenAfterFailure()
		//: the shift already wrapped its own diagnostic.
		return serr
	}
	//: reopen Path fresh through the hardened constructor (CWE-59 re-check).
	f, oerr := openHardened(s.cfg.Path)
	//: a reopen failure leaves the sink unusable — surface it.
	if oerr != nil {
		//: V45, #463: drop the closed descriptor so the next Write reopens Path
		//: (ensureOpen) and the sink self-heals once the transient condition — a
		//: planted link removed, EMFILE or ENOSPC gone — clears. origin wins —
		//: RotFileOpenFailed already set code/reason.
		s.reopenAfterFailure()
		//: surface the reopen failure; the sink will retry the open next Write.
		return oerr
	}
	//: adopt the new descriptor and reset the byte counter.
	s.f = f
	s.size = 0
	//: prune rotated siblings older than the calendar cutoff (no-op when
	//: MaxAgeDays is non-positive); a prune failure surfaces under the rotate
	//: sentinel without losing the freshly reopened descriptor.
	return s.pruneByAge()
}

// reopenAfterFailure best-effort reopens Path through openHardened after a
// rotation step failed with the active descriptor already closed, so the sink
// self-heals instead of latching into a permanently-closed state (V45). On a
// still-failing reopen it drops the closed descriptor — s.f is nil — so the
// next Write, Flush or Rotate opens Path again through ensureOpen rather than
// writing to a dead descriptor forever (#463). The caller holds s.mu and
// returns the original (more informative) error.
func (s *rotatingSink) reopenAfterFailure() {
	s.f = nil
	s.size = 0
	//: try the same hardened open the happy path uses; failure is non-fatal
	//: here — the caller returns the rotation's own error.
	if oerr := s.ensureOpen(); oerr != nil {
		//: no descriptor: the next Write, Flush or Rotate retries the open.
		return
	}
}

// ensureOpen opens Path through openHardened when the sink holds no
// descriptor — its last reopen failed — and re-seeds s.size from the file. It
// is a no-op on a sink that holds one. The caller holds s.mu.
func (s *rotatingSink) ensureOpen() error {
	//: the common case: a live descriptor.
	if s.f != nil {
		//: nothing to reopen.
		return nil
	}
	//: a closed sink stays closed, as one that held its descriptor does.
	if s.closed {
		//: the write a closed descriptor refuses, under the same sentinel.
		return errs.Wrap(os.ErrClosed, errs.WrapParams{
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/observe/logger/writer/rotfile.ensureOpen: the sink is closed",
		})
	}
	f, oerr := openHardened(s.cfg.Path)
	//: still refused: the sink keeps no descriptor and the caller reports why.
	if oerr != nil {
		//: RotFileOpenFailed, with the kind field when a link is the reason.
		return oerr
	}
	//: adopt the recovered descriptor.
	s.f = f
	s.size = 0
	//: an existing file (e.g. a Path.1 rename that never happened) still counts.
	if fi, serr := f.Stat(); serr == nil {
		//: existing content counts toward the next rotation threshold.
		s.size = fi.Size()
	}
	//: open again.
	return nil
}

// shiftBackups renames the rotated siblings down by one (dropping the oldest
// when MaxBackups is reached) and moves the active Path into the .1 slot,
// gzipping it when Compress is set. The caller holds s.mu.
func (s *rotatingSink) shiftBackups() error {
	//: drop the oldest backup first when a finite cap is configured so the
	//: subsequent shift never overflows past MaxBackups.
	if s.cfg.MaxBackups > 0 {
		//: remove the slot we are about to overwrite; ENOENT is fine.
		if rerr := os.Remove(s.backupName(s.cfg.MaxBackups)); rerr != nil && !os.IsNotExist(rerr) {
			//: a real removal failure aborts the rotation.
			return wrapRotate(rerr, "service/observe/logger/writer/rotfile.shiftBackups: dropping oldest backup")
		}
	}
	//: shift the surviving backups down: high index first to avoid clobbering.
	if serr := s.shiftExisting(); serr != nil {
		//: the helper wrapped its own diagnostic.
		return serr
	}
	//: move the just-closed active file into the .1 slot (gzip when requested).
	return s.promoteActive()
}
