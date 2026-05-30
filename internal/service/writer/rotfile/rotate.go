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
		//: the shift already wrapped its own diagnostic.
		return serr
	}
	//: reopen Path fresh through the hardened constructor (CWE-59 re-check).
	f, oerr := openHardened(s.cfg.Path)
	//: a reopen failure leaves the sink unusable — surface it.
	if oerr != nil {
		//: origin wins — RotFileOpenFailed already set the code/reason.
		return oerr
	}
	//: adopt the new descriptor and reset the byte counter.
	s.f = f
	s.size = 0
	//: rotation complete.
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
