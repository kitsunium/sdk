// Package group — the typed fan-out built on the Group.
package group

import "context"

// Collect runs every function in fns concurrently under a [Group] and returns
// their results in SUBMISSION order: index i of the result is fns[i]'s value,
// whatever order the tasks actually finished in.
//
// limit is [New]'s, with the same clamp and the same [Unlimited] spelling.
//
// It is a function rather than a method because Go's methods take no type
// parameters of their own: a Group that both bounded concurrency and carried a
// result type would have to fix that type at construction, forcing
// Group[struct{}] on every caller who only wants to wait. Keeping the result
// type here leaves the common shape untyped and this one exact.
//
// On the first error the remaining tasks see a cancelled context and Collect
// returns (nil, err). No partial slice is handed back: a half-filled result is
// indistinguishable at the call site from a complete one, and the zero values
// in it would read as answers.
//
// A panic in any fn propagates out of Collect exactly as it does out of
// [Group.Wait] — after every other task has returned, carrying the failing
// goroutine's stack.
func Collect[T any](parent context.Context, limit int, fns []func(ctx context.Context) (value T, err error)) (results []T, err error) {
	//: nothing to run, nothing to allocate, and no goroutine to pay for.
	if len(fns) == 0 {
		//: an empty input is not a failure.
		return nil, nil
	}
	//: sized once so each task writes its OWN slot; no append, so no lock.
	out := make([]T, len(fns))
	runner, _ := New(parent, limit)
	//: one task per function, each writing the slot its index owns.
	for i, fn := range fns {
		runner.Go(func(ctx context.Context) error {
			value, err := fn(ctx)
			//: a failing task leaves its slot at the zero value, which is why
			//: the slice below is discarded rather than returned.
			if err != nil {
				//: the group keeps the FIRST of these and cancels the rest.
				return err
			}
			//: disjoint index per task; Wait's WaitGroup is the happens-before
			//: edge that makes every write visible to the caller.
			out[i] = value
			//: this task succeeded.
			return nil
		})
	}
	//: blocks until every task has returned, then reports the first failure.
	if err := runner.Wait(); err != nil {
		//: see above — a partial result set would lie by omission.
		return nil, err
	}
	//: every slot was written by exactly one task.
	return out, nil
}
