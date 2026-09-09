// Package resilience — the per-call bookkeeping of a hedged race.
package resilience

// hedgeRace is the bookkeeping of one hedged call: how many copies are in the
// race, how many have reported, and the earliest failure seen. It is touched
// only by the goroutine running hedge.Run's loop, never by the attempts
// themselves, so it needs no synchronisation of its own.
type hedgeRace struct {
	launched  int
	completed int
	firstErr  error
}

// record folds one attempt's outcome into the race, reporting whether it
// decides the call and — when it does — with what.
func (r *hedgeRace) record(err error) (decided bool, verdict error) {
	//: one attempt reported.
	r.completed++
	//: the first success wins the race outright.
	if err == nil {
		//: the losers are cancelled by Run's deferred cancel.
		return true, nil
	}
	//: keep the earliest failure — by the same first-to-finish rule that
	//: decides a success, no copy is more authoritative than another.
	if r.firstErr == nil {
		//: first failure seen.
		r.firstErr = err
	}
	//: a hedge fires on latency, never on failure, so once every attempt
	//: launched has failed there is nothing left to wait for: report the
	//: operation's own error rather than invent a sentinel that would hide it.
	return r.completed == r.launched, r.firstErr
}
