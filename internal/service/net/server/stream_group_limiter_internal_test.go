// Package server — the per-group connection ceiling.
package server

import (
	"context"
	"errors"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_newConnLimiter pins that ZERO means "no ceiling" rather than "a ceiling
// of zero".
//
// The difference is a working server and a dead one: a limiter built for zero
// would refuse every connection, and every group that never configured MaxConns
// is exactly that case. Returning nil also keeps the policy off the hot path
// entirely for the common group.
func Test_newConnLimiter(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// maxConns is the group's configured ceiling.
		maxConns int
		// wantLimiter is whether a ceiling must be installed.
		wantLimiter bool
	}
	tests := []tc{
		//: no ceiling, so the hot path skips the policy entirely.
		{name: "unset means no ceiling", maxConns: 0},
		{name: "a negative ceiling means no ceiling", maxConns: -1},
		{name: "a ceiling of one", maxConns: 1, wantLimiter: true},
		{name: "a large ceiling", maxConns: 4096, wantLimiter: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.maxConns})

		if !c.wantLimiter {
			if limiter != nil {
				t.Fatalf("a group with MaxConns=%d got a ceiling — it would refuse "+
					"every connection", c.maxConns)
			}
			return
		}
		if limiter == nil {
			t.Fatalf("a group with MaxConns=%d got no ceiling", c.maxConns)
		}
		if limiter.runner == nil {
			t.Error("the ceiling has no runner to admit or reject with")
		}
		//: the ceiling is carried alongside so a refusal can name the number it
		//: hit, which is what an operator needs to decide whether to raise it.
		if limiter.ceiling != c.maxConns {
			t.Errorf("the ceiling records %d, want %d", limiter.ceiling, c.maxConns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_admit pins that a connection turned away is answered in the NET
// domain's terms and counted.
//
// Goroutine lifecycle: one goroutine per occupied slot, each blocked in a
// handler until the case's release channel closes on the way out. Every one is
// confirmed started before the admission under test, so the ceiling is genuinely
// full rather than racily so.
//
// The bulkhead from the resilience domain is reused rather than reimplemented —
// a connection ceiling is exactly a reject-mode semaphore — but its sentinel
// must not escape: a net consumer has no reason to know the resilience domain
// exists. And the refusal is counted, because RejectedConns is how an operator
// tells a saturated server from an idle one.
func Test_Server_admit(t *testing.T) {
	t.Parallel()
	failure := errors.New("handler failed")

	type tc struct {
		// name describes the case.
		name string
		// ceiling is the group's configured maximum; zero means none.
		ceiling int
		// occupy holds this many slots before the admission under test.
		occupy int
		// handlerErr is what the handler reports once admitted.
		handlerErr error
		// wantAdmitted is whether the handler must run at all.
		wantAdmitted bool
	}
	tests := []tc{
		{name: "no ceiling at all", wantAdmitted: true},
		{name: "a free slot", ceiling: 2, wantAdmitted: true},
		{name: "the last free slot", ceiling: 2, occupy: 1, wantAdmitted: true},
		//: accepted by the kernel, then refused by the ceiling.
		{name: "no slot left", ceiling: 2, occupy: 2},
		{name: "a ceiling of one, already taken", ceiling: 1, occupy: 1},
		{name: "a handler that fails", ceiling: 2, handlerErr: failure, wantAdmitted: true},
		{name: "a handler that fails with no ceiling", handlerErr: failure, wantAdmitted: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.ceiling})
		release := make(chan struct{})
		defer close(release)
		held := make(chan struct{}, c.occupy)
		//: occupy slots with handlers that stay in them for the test's duration.
		for range c.occupy {
			go func() {
				swallowErr(srv.admit(t.Context(), limiter,
					&conn{Conn: &fakeSocket{}},
					corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
						held <- struct{}{}
						<-release
						return nil
					})))
			}()
		}
		for range c.occupy {
			select {
			case <-held:
			case <-time.After(serveDeadline):
				t.Fatal("a slot-holding handler never started")
			}
		}

		var ran bool
		err := srv.admit(t.Context(), limiter, &conn{Conn: &fakeSocket{}},
			corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
				ran = true
				return c.handlerErr
			}))

		if !c.wantAdmitted {
			if ran {
				t.Fatal("the handler ran for a connection past the ceiling")
			}
			//: the domain's own sentinel, so a net consumer never meets a
			//: resilience one it has no reason to know about.
			if !errs.HasCode(err, corenet.CodeConnLimitReached) {
				t.Fatalf("admit = %v, want CONN_LIMIT_REACHED", err)
			}
			//: the refusal names the ceiling it hit.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "limit" {
					named = true
				}
			}
			if !named {
				t.Errorf("the refusal does not name the ceiling: %v", errs.FieldsOf(err))
			}
			//: counted, or an operator cannot see saturation at all.
			if srv.State().RejectedConns != 1 {
				t.Errorf("RejectedConns = %d, want 1", srv.State().RejectedConns)
			}
			return
		}
		if !ran {
			t.Fatal("an admitted connection never reached the handler")
		}
		//: the handler's own outcome travels back unchanged.
		if !errors.Is(err, c.handlerErr) {
			t.Fatalf("admit = %v, want %v", err, c.handlerErr)
		}
		//: an admitted connection is not a rejection.
		if srv.State().RejectedConns != 0 {
			t.Errorf("RejectedConns = %d for an admitted connection", srv.State().RejectedConns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_admit_ReleasesItsSlot pins that a served connection gives its slot
// back. A ceiling that leaked slots would wedge the group after MaxConns
// connections, which is far worse than having no ceiling at all.
func Test_Server_admit_ReleasesItsSlot(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// ceiling is the group's configured maximum.
		ceiling int
		// rounds is how many connections pass through it, one after another.
		rounds int
	}
	tests := []tc{
		{name: "a ceiling of one", ceiling: 1, rounds: 20},
		{name: "a ceiling of four", ceiling: 4, rounds: 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		limiter := newConnLimiter(corenet.LimitsValue{MaxConns: c.ceiling})
		handler := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil })

		//: strictly sequential, so every slot is free again before the next
		//: connection asks for one — a leak wedges the group well before the end.
		for i := range c.rounds {
			if err := srv.admit(t.Context(), limiter, &conn{Conn: &fakeSocket{}}, handler); err != nil {
				t.Fatalf("connection %d of %d was refused under a ceiling of %d: %v — "+
					"a slot leaked", i, c.rounds, c.ceiling, err)
			}
		}
		if rejected := srv.State().RejectedConns; rejected != 0 {
			t.Fatalf("RejectedConns = %d for sequential traffic under a ceiling of %d, want 0",
				rejected, c.ceiling)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
