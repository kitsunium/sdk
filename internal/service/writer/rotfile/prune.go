// Package rotfile — calendar pruning of rotated siblings (MaxAgeDays), split
// out of rotate.go so each function stays under KTN-FUNC-MAXLOC. The exported
// on-demand Rotate method lives beside its receiver in rotating_sink.go.
package rotfile

import (
	"os"
	"time"
)

// pruneByAge removes rotated siblings whose modification time is older than the
// MaxAgeDays cutoff, evaluated against the injected clock. It is a no-op when
// MaxAgeDays is non-positive. The numeric .N naming is unchanged; the cutoff is
// derived from each sibling's on-disk mtime rather than a name-embedded stamp.
// The caller holds s.mu.
func (s *rotatingSink) pruneByAge() error {
	//: a non-positive MaxAgeDays disables calendar pruning entirely.
	if s.cfg.MaxAgeDays <= 0 {
		//: nothing to prune — keep every rotated sibling.
		return nil
	}
	//: compute the cutoff once from the injected clock so the whole sweep
	//: uses a single, deterministic "now".
	cutoff := s.clk.Now().AddDate(0, 0, -s.cfg.MaxAgeDays)
	//: walk the contiguous numeric slots upward, stopping at the first gap so
	//: an unbounded history (MaxBackups == 0) still terminates.
	for n := 1; ; n++ {
		more, perr := s.pruneSlotByAge(n, cutoff)
		//: a removal failure aborts the sweep with the rotate sentinel.
		if perr != nil {
			//: surface the wrapped diagnostic to the caller.
			return perr
		}
		//: a missing slot ends the contiguous run of siblings.
		if !more {
			//: sweep complete — no further slots to consider.
			return nil
		}
	}
}

// pruneSlotByAge removes slot n when its mtime predates cutoff. It reports
// whether the sweep should continue: false once a slot is absent (the
// contiguous backup history has ended). The caller holds s.mu.
func (s *rotatingSink) pruneSlotByAge(n int, cutoff time.Time) (more bool, err error) {
	name := s.backupName(n)
	fi, serr := os.Lstat(name)
	//: a missing slot ends the contiguous run; stop the sweep.
	if serr != nil {
		//: no file at this slot — signal the caller to stop.
		return false, nil
	}
	//: keep siblings at or after the cutoff; only older ones are pruned.
	if !fi.ModTime().Before(cutoff) {
		//: within the retention window — leave it and keep scanning.
		return true, nil
	}
	//: the sibling predates the cutoff — remove it (ENOENT is tolerated).
	if rerr := os.Remove(name); rerr != nil && !os.IsNotExist(rerr) {
		//: a real removal failure surfaces under the rotate sentinel.
		return false, wrapRotate(rerr, "service/writer/rotfile.pruneSlotByAge: removing aged backup")
	}
	//: pruned (or already gone) — continue with the next slot.
	return true, nil
}
