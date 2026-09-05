// Package server is the inbound half of the SDK's network domain.
package server

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_newPooledConn pins that the recycler's constructor hands back a ZERO
// value.
//
// acquire fills every field, so anything set here would be either overwritten —
// wasted work on the accept path — or, worse, left behind on a field acquire
// does not touch, where it would surface as one connection wearing another's
// state.
func Test_newPooledConn(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// mints is how many wrappers are taken, since a pool hands out many.
		mints int
	}
	tests := []tc{
		{name: "one wrapper", mints: 1},
		{name: "several wrappers", mints: 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[*conn]bool, c.mints)
		for range c.mints {
			wrapper := newPooledConn()

			if wrapper == nil {
				t.Fatal("newPooledConn returned nothing for the pool to hand out")
			}
			//: a zero value is correct here precisely because acquire fills
			//: every field; anything else is either wasted or left behind.
			if wrapper.Conn != nil || wrapper.id != 0 || wrapper.group != "" ||
				wrapper.scratch != nil || wrapper.timeouts != (corenet.TimeoutsValue{}) {
				t.Fatalf("a freshly minted wrapper arrives pre-filled: %+v", wrapper)
			}
			//: each is its own value, or two connections would share one wrapper.
			if seen[wrapper] {
				t.Fatal("newPooledConn handed back the same wrapper twice")
			}
			seen[wrapper] = true
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_recordDeclError pins that the FIRST declaration error is the one
// kept.
//
// The first is the one that explains the others: a duplicate group name makes
// every later declaration on that name look wrong too, and reporting the last
// would name a symptom. Keeping one also means Group and PacketGroup can stay
// error-free in their signatures, which is what keeps wiring a server down to a
// handful of readable lines.
func Test_Server_recordDeclError(t *testing.T) {
	t.Parallel()
	first := errs.Wrap(corenet.GroupDuplicate, errs.WrapParams{}, errs.String("group", "api"))
	second := errs.Wrap(corenet.InvalidAddress, errs.WrapParams{}, errs.String("group", "admin"))

	type tc struct {
		// name describes the case.
		name string
		// recorded are the errors handed in, in order.
		recorded []error
		// wantCode is the code Start must eventually report.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "nothing went wrong"},
		{name: "one mistake", recorded: []error{first}, wantCode: corenet.CodeGroupDuplicate},
		//: the first is the one that explains the others.
		{name: "two mistakes", recorded: []error{first, second}, wantCode: corenet.CodeGroupDuplicate},
		{name: "the other order", recorded: []error{second, first}, wantCode: corenet.CodeInvalidAddress},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		srv.mu.Lock()
		for _, err := range c.recorded {
			srv.recordDeclError(err)
		}
		got := srv.declErr
		srv.mu.Unlock()

		if c.wantCode == 0 {
			if got != nil {
				t.Fatalf("a server with no declaration mistake recorded %v", got)
			}
			return
		}
		if !errs.HasCode(got, c.wantCode) {
			t.Fatalf("the recorded error is %v, want code %v", got, c.wantCode)
		}
		//: and Start is where it surfaces, which is the whole reason Group
		//: returns no error of its own.
		if err := srv.Start(t.Context()); !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Start = %v, want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
