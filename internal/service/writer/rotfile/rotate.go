// Package rotfile — rotation cycle: threshold check, backup shift, optional
// gzip, and the hardened reopen. Kept out of sink.go so each function stays
// under KTN-FUNC-MAXLOC.
package rotfile

import "os"

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
	//: close the active descriptor before renaming it.
	if cerr := s.f.Close(); cerr != nil {
		//: surface the close failure under the rotate sentinel.
		return wrapRotate(cerr, "service/writer/rotfile.rotate: closing active file")
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
		//: V45: re-seed size to 0 so the next maybeRotate does not immediately
		//: re-enter rotate() on the still-closed descriptor; the next Write retries
		//: the open via reopenAfterFailure and the sink self-heals once the transient
		//: condition clears. origin wins — RotFileOpenFailed already set code/reason.
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
// self-heals on the next Write instead of latching into a permanently-closed
// state (V45). It re-seeds s.size from the reopened file; on a still-failing
// reopen it resets s.size to 0 so the next maybeRotate does not immediately
// re-enter rotate() on the dead descriptor — the next Write retries the open.
// The caller holds s.mu and returns the original (more informative) error.
func (s *rotatingSink) reopenAfterFailure() {
	//: try the same hardened open the happy path uses; failure is non-fatal here.
	f, oerr := openHardened(s.cfg.Path)
	//: a still-failing reopen leaves no usable descriptor — reset size so the
	//: next Write retries the open rather than thrashing rotate() on a dead fd.
	if oerr != nil {
		//: keep s.f as-is (already closed) but neutralise the size trigger.
		s.size = 0
		//: nothing more to recover here; the next Write retries the open.
		return
	}
	//: adopt the recovered descriptor in place of the closed one.
	s.f = f
	//: re-seed size from the reopened file so the next threshold check is honest.
	s.size = 0
	//: an existing file (e.g. a Path.1 rename that never happened) still counts.
	if fi, serr := f.Stat(); serr == nil {
		//: existing content counts toward the next rotation threshold.
		s.size = fi.Size()
	}
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
			return wrapRotate(rerr, "service/writer/rotfile.shiftBackups: dropping oldest backup")
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
