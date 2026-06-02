// Package rotfile — the interval-rotation tick body driven by the worker.Every
// daemon wired in newRotatingSink when Config.RotateEvery is positive. Split
// from rotating_sink.go so the daemon's tick logic and its tests sit in their
// own source/test pair.
package rotfile

// tickRotate is the interval-rotation tick body run on the daemon goroutine. It
// forces a Rotate and, on failure, stashes the typed error under mu so the next
// Write surfaces it exactly once — the tick never logs or discards (a silent
// drop would violate KTN-ERROR-DISCARD and hide a failing archive policy).
func (s *rotatingSink) tickRotate() {
	//: force the time-triggered rotation (Rotate takes mu itself, so this must
	//: NOT be called under mu — it never is, the daemon runs free of the lock).
	rerr := s.Rotate()
	//: a healthy tick has nothing to stash.
	if rerr == nil {
		//: rotation succeeded — leave any prior stash untouched.
		return
	}
	//: stash the first failure so a later Write can surface it once; keep the
	//: earliest error rather than clobbering it with every subsequent tick.
	s.mu.Lock()
	//: only record when no error is pending surfacing yet.
	if s.tickErr == nil {
		//: hand the typed rotate error to the next Write.
		s.tickErr = rerr
	}
	s.mu.Unlock()
}
