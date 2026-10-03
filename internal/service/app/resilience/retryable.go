// Package resilience — shared retryable-error classification helper.
package resilience

// isRetryable reports whether err is a transient failure the policy may act on
// (replay it, or count it against a dependency's health). A nil predicate keeps
// the pre-classifier contract where every error is transient; the predicate is
// never called with a nil error because both call sites gate on err != nil
// first.
func isRetryable(pred func(error) bool, err error) bool {
	//: no classifier configured — every error stays transient (historic default).
	if pred == nil {
		//: unconditionally transient.
		return true
	}
	//: delegate the verdict to the caller-supplied classifier.
	return pred(err)
}
