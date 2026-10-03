// Package rotfile — the exported on-demand Rotate method (SIGHUP / logrotate
// integration), split out of rotating_sink.go so the receiver file stays under
// KTN-FUNC-MAXLOC. The calendar-pruning helpers it delegates to live in
// prune.go.
package rotfile

// Rotate forces an immediate rotation of the active file.
//
// It applies the MaxBackups and MaxAgeDays retention policies after the cut and
// is meant for SIGHUP / logrotate integration where the caller wires the signal
// to this method. Rotate is a no-op on an empty active file (calendar pruning of
// existing siblings still runs) and is safe for concurrent use, serialising
// against Write through the same mutex.
func (s *rotatingSink) Rotate() error {
	//: serialise against in-flight writes so the close/shift/reopen is atomic.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: an empty active file is never rotated into an empty backup; still run
	//: age pruning so an idle sink's old siblings eventually expire.
	if s.size == 0 {
		//: calendar pruning advances even when there is nothing to rotate.
		return s.pruneByAge()
	}
	//: a non-empty file performs the full close/shift/reopen + prune cycle.
	return s.rotate()
}
