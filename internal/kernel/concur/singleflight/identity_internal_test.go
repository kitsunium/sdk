package singleflight

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// This file is #813's identity proof. Since #813 the shared call's context is
// built over a root kept inside the call record rather than over
// context.WithoutCancel(leader): one allocation fewer per leading call, and
// nothing else may change. Every test below compares what a caller and fn
// observe with what the construction before #813 —
// context.WithCancel(context.WithoutCancel(leader)), built here as the
// reference — shows at the same point: the values, the deadline, Done, Err,
// Cause, the name, the children and an AfterFunc, and Do's own values, shared
// flags, errors and panics. No test names the root, so the file compiles
// against the code before #813 as against the code after, and passes on both.

// valueKey is the key of the value that proves whose values the shared call
// keeps; otherKey the one a child adds.
type (
	valueKey struct{}
	otherKey struct{}
)

// opaqueCtx is a caller's context type with no String method of its own: the
// context package names it by its type.
type opaqueCtx struct{ context.Context }

// namedCtx is a caller's context type that names itself.
type namedCtx struct{ context.Context }

// String is namedCtx's own name.
func (namedCtx) String() string {
	//: a name the context package prints verbatim.
	return "a named context"
}

// errIdentity is the failure fn returns, compared by identity; errCause the
// cause a caller cancels with, which must stay that caller's.
var (
	errIdentity = errors.New("identity")
	errCause    = errors.New("the caller's own cause")
)

// pollUntil spins until cond holds, or fails the test.
func pollUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ctxView is everything a holder of a context.Context can observe of it at
// one instant.
type ctxView struct {
	value, other   any
	deadline, done bool
	err, cause     error
	name           string
}

// viewOf reads every observable of ctx at once.
func viewOf(ctx context.Context) ctxView {
	_, deadline := ctx.Deadline()
	done := false
	//: a nil Done is never closed; a closed one is read without blocking.
	if ch := ctx.Done(); ch != nil {
		select {
		case <-ch:
			done = true
		default:
		}
	}
	//: the whole view, its name without the instants no two builds share.
	return ctxView{
		value:    ctx.Value(valueKey{}),
		other:    ctx.Value(otherKey{}),
		deadline: deadline,
		done:     done,
		err:      ctx.Err(),
		cause:    context.Cause(ctx),
		name:     undated(fmt.Sprint(ctx)),
	}
}

// undated drops what a context's name says of a deadline — its instant and
// the time left, which two contexts built a moment apart never share — and
// keeps the chain of how it was built.
func undated(name string) string {
	const mark = "WithDeadline("
	var out strings.Builder
	//: each deadline's contents, up to its closing parenthesis, are cut.
	for {
		at := strings.Index(name, mark)
		//: no deadline left in the name.
		if at < 0 {
			out.WriteString(name)
			//: the chain, undated.
			return out.String()
		}
		out.WriteString(name[:at+len(mark)])
		name = name[at+len(mark):]
		//: the instant and the time left end at the first parenthesis.
		if end := strings.IndexByte(name, ')'); end >= 0 {
			name = name[end:]
		}
	}
}

// family is a context and what fn may derive from it: a cancelable child, a
// child with a deadline of its own, a child with one more value, a child
// without cancel — and a function registered to run once the context is done.
type family struct {
	members [5]context.Context
	fired   chan struct{}
}

// familyView is what a family shows at one instant.
type familyView struct {
	members [5]ctxView
	fired   bool
}

// familyOf derives ctx's family; the test releases its children.
func familyOf(t *testing.T, ctx context.Context) *family {
	child, cancelChild := context.WithCancel(ctx)
	t.Cleanup(cancelChild)
	timed, cancelTimed := context.WithTimeout(ctx, time.Hour)
	t.Cleanup(cancelTimed)
	built := &family{fired: make(chan struct{})}
	built.members = [5]context.Context{
		ctx, child, timed, context.WithValue(ctx, otherKey{}, "child"), context.WithoutCancel(ctx),
	}
	//: AfterFunc runs its function on a goroutine of its own once ctx is done.
	stop := context.AfterFunc(ctx, func() { close(built.fired) })
	t.Cleanup(func() { stop() })
	//: the family, every member live while ctx is.
	return built
}

// view reads every member, and whether the function registered on the family's
// root has run — waited for once the root is done, since it runs on a
// goroutine of its own. It may run on fn's goroutine, so it reports with
// t.Error and never stops the test.
func (f *family) view(t *testing.T) familyView {
	t.Helper()
	var seen familyView
	for at, member := range f.members {
		seen.members[at] = viewOf(member)
	}
	//: a done root has run, or is about to run, its function.
	if seen.members[0].done {
		select {
		case <-f.fired:
		case <-time.After(5 * time.Second):
			t.Error("the function registered with context.AfterFunc never ran")
		}
	}
	select {
	case <-f.fired:
		seen.fired = true
	default:
	}
	//: the whole family at this instant.
	return seen
}

// referenceOf builds the shared call's context the way singleflight built it
// before #813: the yardstick every view is compared with.
func referenceOf(leader context.Context) (context.Context, context.CancelFunc) {
	//: the construction #813 replaced.
	return context.WithCancel(context.WithoutCancel(leader))
}

// compare fails the test when got and want differ, member by member.
func compare(t *testing.T, at string, got, want familyView) {
	t.Helper()
	//: one report per member that differs.
	for member := range got.members {
		if got.members[member] != want.members[member] {
			t.Errorf("%s, member %d:\n got %+v\nwant %+v", at, member, got.members[member], want.members[member])
		}
	}
	if got.fired != want.fired {
		t.Errorf("%s: AfterFunc ran = %v, want %v", at, got.fired, want.fired)
	}
}

// outcome is what one Do returned.
type outcome struct {
	got    string
	shared bool
	err    error
}

// TestSharedContextReadsAsWithoutCancelOverEveryParent runs one call over each
// shape of leader context and compares fn's context and its family with the
// reference built over the same leader — while fn runs, and once Do has
// returned and the call released its context — and checks Do's own result.
func TestSharedContextReadsAsWithoutCancelOverEveryParent(t *testing.T) {
	t.Parallel()
	valued := func(t *testing.T) context.Context {
		return context.WithValue(context.WithValue(t.Context(), valueKey{}, "leader"), otherKey{}, 7)
	}
	parents := []struct {
		name  string
		build func(t *testing.T) context.Context
	}{
		{"background", func(*testing.T) context.Context { return context.Background() }},
		{"todo", func(*testing.T) context.Context { return context.TODO() }},
		{"values", valued},
		{"deadline", func(t *testing.T) context.Context {
			ctx, cancel := context.WithTimeout(valued(t), time.Hour)
			t.Cleanup(cancel)
			return ctx
		}},
		{"cancel with cause", func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancelCause(valued(t))
			t.Cleanup(func() { cancel(errCause) })
			return ctx
		}},
		{"without cancel", func(t *testing.T) context.Context { return context.WithoutCancel(valued(t)) }},
		{"a type with no name", func(t *testing.T) context.Context { return opaqueCtx{valued(t)} }},
		{"a type with a name", func(t *testing.T) context.Context { return namedCtx{valued(t)} }},
	}
	for _, parent := range parents {
		t.Run(parent.name, func(t *testing.T) {
			t.Parallel()
			var group Group[string, string]
			leader := parent.build(t)
			ref, refCancel := referenceOf(leader)
			refFamily := familyOf(t, ref)
			wantLive := refFamily.view(t)
			var kept *family
			var live familyView
			got, shared, err := group.Do(leader, "k", func(ctx context.Context) (string, error) {
				kept = familyOf(t, ctx)
				live = kept.view(t)
				return "value", nil
			})
			if got != "value" || shared || err != nil {
				t.Errorf("Do = %q, %v, %v; want value, false, nil", got, shared, err)
			}
			compare(t, "while fn runs", live, wantLive)
			//: once fn has returned the call releases its context, as the
			//: cancel before #813 did.
			pollUntil(t, func() bool { return kept.members[0].Err() != nil }, "the released context")
			refCancel()
			compare(t, "once fn has returned", kept.view(t), refFamily.view(t))
		})
	}
}

// TestDoReturnsWhatItReturnedBeforeTheChange pins Do's own outputs: fn's value
// and error untouched, the same error to every caller sharing the call, the
// same shared flags, fn's panic re-raised with its value, and a nil context
// refused with the panic context.WithoutCancel raised.
func TestDoReturnsWhatItReturnedBeforeTheChange(t *testing.T) {
	t.Parallel()
	t.Run("fn fails", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		got, shared, err := group.Do(t.Context(), "k", func(context.Context) (string, error) {
			return "partial", errIdentity
		})
		//: the value and the error are fn's, both, untouched.
		if got != "partial" || shared || err != errIdentity {
			t.Errorf("Do = %q, %v, %v; want partial, false, errIdentity itself", got, shared, err)
		}
	})
	t.Run("fn fails for two callers", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		release, started := make(chan struct{}), make(chan struct{})
		leaderOut, followerOut := make(chan outcome, 1), make(chan outcome, 1)
		fn := func(context.Context) (string, error) {
			close(started)
			<-release
			return "partial", errIdentity
		}
		go func() {
			got, shared, err := group.Do(t.Context(), "k", fn)
			leaderOut <- outcome{got, shared, err}
		}()
		<-started
		go func() {
			got, shared, err := group.Do(t.Context(), "k", fn)
			followerOut <- outcome{got, shared, err}
		}()
		awaitJoined(t, &group, "k", 2)
		close(release)
		//: the same value and the same error for both; only the leader led.
		if out := <-leaderOut; out != (outcome{"partial", false, errIdentity}) {
			t.Errorf("leader's Do = %+v; want partial, false, errIdentity", out)
		}
		if out := <-followerOut; out != (outcome{"partial", true, errIdentity}) {
			t.Errorf("follower's Do = %+v; want partial, true, errIdentity", out)
		}
	})
	t.Run("fn panics", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		var raised any
		func() {
			defer func() { raised = recover() }()
			_, _, _ = group.Do(t.Context(), "k", func(context.Context) (string, error) {
				panic(errIdentity)
			})
		}()
		//: the waiter panics with a PanicValue carrying fn's own value.
		if carried, ok := raised.(PanicValue); !ok || carried.Raised != errIdentity {
			t.Errorf("Do panicked with %#v; want a PanicValue raising errIdentity", raised)
		}
	})
	t.Run("a nil context", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		var want, got any
		func() {
			defer func() { want = recover() }()
			_ = context.WithoutCancel(nil) //nolint:staticcheck // the panic of a nil parent is the yardstick
		}()
		func() {
			defer func() { got = recover() }()
			//nolint:staticcheck // the nil context is the case under test
			_, _, _ = group.Do(nil, "k", func(context.Context) (string, error) { return "", nil })
		}()
		if got == nil || got != want {
			t.Errorf("Do(nil) panicked with %v; want %v", got, want)
		}
	})
}

// TestCancellationReachesTheSharedCallAsBefore pins the cancellation each
// caller and fn observe — a lone caller cancelled, a lone caller's deadline
// passed, the first of two callers leaving, the last of two leaving — against
// the reference built over the leader's context and cancelled where the Group
// cancels.
func TestCancellationReachesTheSharedCallAsBefore(t *testing.T) {
	t.Parallel()
	t.Run("a lone caller cancelled with a cause", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		caller, cancel := context.WithCancelCause(context.WithValue(t.Context(), valueKey{}, "leaver"))
		ref, refCancel := referenceOf(caller)
		refFamily := familyOf(t, ref)
		started, observed := make(chan *family, 1), make(chan familyView, 1)
		out := make(chan outcome, 1)
		go func() {
			got, shared, err := group.Do(caller, "k", func(ctx context.Context) (string, error) {
				kept := familyOf(t, ctx)
				started <- kept
				<-ctx.Done()
				observed <- kept.view(t)
				return "late", ctx.Err()
			})
			out <- outcome{got, shared, err}
		}()
		kept := <-started
		//: the cause is the caller's; the call does not see it.
		cancel(errCause)
		//: the caller's own error, never its cause; a zero value; not shared.
		if got := <-out; got != (outcome{"", false, context.Canceled}) {
			t.Errorf("Do = %+v; want \"\", false, context.Canceled", got)
		}
		//: the caller was the last out: the call is cancelled before Do returns.
		if kept.members[0].Err() == nil {
			t.Error("the call's context is live once its last caller has left")
		}
		refCancel()
		compare(t, "once the caller left", <-observed, refFamily.view(t))
	})
	t.Run("a lone caller past its deadline", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		caller, cancel := context.WithDeadline(context.WithValue(t.Context(), valueKey{}, "late"), time.Unix(0, 0))
		defer cancel()
		ref, refCancel := referenceOf(caller)
		refFamily := familyOf(t, ref)
		observed := make(chan familyView, 1)
		got, shared, err := group.Do(caller, "k", func(ctx context.Context) (string, error) {
			kept := familyOf(t, ctx)
			<-ctx.Done()
			observed <- kept.view(t)
			return "late", ctx.Err()
		})
		//: the caller's own deadline, a zero value, not shared.
		if got != "" || shared || err != context.DeadlineExceeded {
			t.Errorf("Do = %q, %v, %v; want \"\", false, context.DeadlineExceeded", got, shared, err)
		}
		refCancel()
		compare(t, "once the caller's deadline passed", <-observed, refFamily.view(t))
	})
	t.Run("two callers, the first leaves", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		leader, leaderCancel := context.WithTimeout(context.WithValue(t.Context(), valueKey{}, "leader"), time.Hour)
		defer leaderCancel()
		follower := context.WithValue(t.Context(), valueKey{}, "follower")
		ref, refCancel := referenceOf(leader)
		refFamily := familyOf(t, ref)
		release, started := make(chan struct{}), make(chan *family, 1)
		observed := make(chan familyView, 1)
		leaderOut, followerOut := make(chan outcome, 1), make(chan outcome, 1)
		fn := func(ctx context.Context) (string, error) {
			kept := familyOf(t, ctx)
			started <- kept
			<-release
			observed <- kept.view(t)
			return "shared value", nil
		}
		go func() {
			got, shared, err := group.Do(leader, "k", fn)
			leaderOut <- outcome{got, shared, err}
		}()
		kept := <-started
		compare(t, "one caller in", kept.view(t), refFamily.view(t))
		go func() {
			got, shared, err := group.Do(follower, "k", fn)
			followerOut <- outcome{got, shared, err}
		}()
		awaitJoined(t, &group, "k", 2)
		//: the leader leaves; the follower still waits, so the call lives on.
		leaderCancel()
		if out := <-leaderOut; out != (outcome{"", false, context.Canceled}) {
			t.Errorf("leader's Do = %+v; want \"\", false, context.Canceled", out)
		}
		compare(t, "the leader gone", kept.view(t), refFamily.view(t))
		close(release)
		compare(t, "as fn returns", <-observed, refFamily.view(t))
		//: the follower gets the leader's call: its value, shared.
		if out := <-followerOut; out != (outcome{"shared value", true, nil}) {
			t.Errorf("follower's Do = %+v; want shared value, true, nil", out)
		}
		pollUntil(t, func() bool { return kept.members[0].Err() != nil }, "the released context")
		refCancel()
		compare(t, "once fn has returned", kept.view(t), refFamily.view(t))
	})
	t.Run("two callers, the last leaves", func(t *testing.T) {
		t.Parallel()
		var group Group[string, string]
		leader, leaderCancel := context.WithCancel(context.WithValue(t.Context(), valueKey{}, "leader"))
		defer leaderCancel()
		follower, followerCancel := context.WithCancelCause(context.WithValue(t.Context(), valueKey{}, "follower"))
		defer followerCancel(nil)
		ref, refCancel := referenceOf(leader)
		refFamily := familyOf(t, ref)
		started, observed := make(chan *family, 1), make(chan familyView, 1)
		leaderOut, followerOut := make(chan outcome, 1), make(chan outcome, 1)
		fn := func(ctx context.Context) (string, error) {
			kept := familyOf(t, ctx)
			started <- kept
			<-ctx.Done()
			observed <- kept.view(t)
			return "late", ctx.Err()
		}
		go func() {
			got, shared, err := group.Do(leader, "k", fn)
			leaderOut <- outcome{got, shared, err}
		}()
		kept := <-started
		go func() {
			got, shared, err := group.Do(follower, "k", fn)
			followerOut <- outcome{got, shared, err}
		}()
		awaitJoined(t, &group, "k", 2)
		//: the first out leaves the call running for the other.
		leaderCancel()
		if out := <-leaderOut; out != (outcome{"", false, context.Canceled}) {
			t.Errorf("leader's Do = %+v; want \"\", false, context.Canceled", out)
		}
		compare(t, "the first caller gone", kept.view(t), refFamily.view(t))
		//: the last out cancels the call, before its Do returns.
		followerCancel(errCause)
		if out := <-followerOut; out != (outcome{"", true, context.Canceled}) {
			t.Errorf("follower's Do = %+v; want \"\", true, context.Canceled", out)
		}
		if kept.members[0].Err() == nil {
			t.Error("the call's context is live once its last caller has left")
		}
		refCancel()
		compare(t, "the last caller gone", <-observed, refFamily.view(t))
	})
}
