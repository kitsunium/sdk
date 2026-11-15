package consensus

import "slices"

// ---- the targets a joiner tries, the leader named next (#1422) ----

// joinFollows bounds how many leaders one JOIN follows past its configured
// targets: a learner names the leader once, and a leadership that moved
// while the JOIN went round names one more (#1422).
const joinFollows int = 2

// joinQueue is the targets a joiner sends its JOIN to, in turn: the ones it
// was configured with, and the leader a member that refused it NOT_LEADER
// names, tried next (#1422). Each target is tried once.
type joinQueue[K comparable] struct {
	todo    []K
	tried   map[K]bool
	follows int
}

// newJoinQueue is a queue over targets, in their order.
func newJoinQueue[K comparable](targets []K) *joinQueue[K] {
	return &joinQueue[K]{todo: slices.Clone(targets), tried: make(map[K]bool, len(targets))}
}

// next is the next target not tried yet; ok is false once none is left.
func (q *joinQueue[K]) next() (k K, ok bool) {
	for len(q.todo) > 0 {
		k, q.todo = q.todo[0], q.todo[1:]
		if !q.tried[k] {
			q.tried[k] = true
			return k, true
		}
	}
	return k, false
}

// follow puts leader first among the targets left, unless it was tried or
// the queue followed joinFollows leaders already.
func (q *joinQueue[K]) follow(leader K) {
	if q.tried[leader] || q.follows == joinFollows {
		return
	}
	q.follows++
	q.todo = slices.Insert(q.todo, 0, leader)
}
