// Package client — the policy conjunction.
package client

import (
	"errors"
	"net/http"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubPolicy returns a fixed verdict and records that it was consulted.
type stubPolicy struct {
	err   error
	calls *int
}

// Allow records the call and returns the fixed verdict.
func (p stubPolicy) Allow(corenet.RequestValue) error {
	*p.calls++
	return p.err
}

// Test_conjunction_Allow pins the two properties that make composition safe.
//
// Every member must allow, so adding a policy can only ever NARROW what is
// permitted — that is what lets a caller be handed a client and add their own
// rules without any risk of widening it. And an EMPTY conjunction refuses,
// because a composition that lost its members must close: a caller whose policy
// list came from a config that failed to load would otherwise get an
// unrestricted client while believing it was guarded.
func Test_conjunction_Allow(t *testing.T) {
	t.Parallel()
	denied := errs.Wrap(corenet.RequestDenied, errs.WrapParams{}, errs.String("why", "no"))
	unsafe := errs.Wrap(corenet.UnsafePath, errs.WrapParams{}, errs.String("why", "no"))

	type tc struct {
		name string
		//: each member's verdict, in order.
		verdicts []error
		//: how many members must actually be consulted.
		wantCalls int
		//: the code the conjunction must report, or zero to allow.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "no members refuse", wantCode: corenet.CodeRequestDenied},
		{name: "a single allowing member", verdicts: []error{nil}, wantCalls: 1},
		{name: "every member allows", verdicts: []error{nil, nil, nil}, wantCalls: 3},
		{
			//: the first refusal is final, and the members after it are never
			//: consulted — a policy must not be able to overturn a denial.
			name:      "the first member refuses",
			verdicts:  []error{denied, nil, nil},
			wantCalls: 1,
			wantCode:  corenet.CodeRequestDenied,
		},
		{
			name:      "a later member refuses",
			verdicts:  []error{nil, denied, nil},
			wantCalls: 2,
			wantCode:  corenet.CodeRequestDenied,
		},
		{
			//: the member's OWN code survives, so a caller can tell an unsafe
			//: path from a policy denial.
			name:      "an unsafe-path refusal keeps its code",
			verdicts:  []error{nil, unsafe},
			wantCalls: 2,
			wantCode:  corenet.CodeUnsafePath,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		calls := 0
		members := make([]corenet.Policy, 0, len(c.verdicts))
		for _, v := range c.verdicts {
			members = append(members, stubPolicy{err: v, calls: &calls})
		}
		p := &conjunction{members: members}

		err := p.Allow(corenet.RequestValue{Method: http.MethodGet, EscapedPath: "/v1/x"})

		if calls != c.wantCalls {
			t.Errorf("%d members were consulted, want %d", calls, c.wantCalls)
		}
		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("Allow = %v, want nil", err)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Allow = %v, want code %v", err, c.wantCode)
		}
		//: a member's refusal is returned unchanged, so its "why" reaches the
		//: caller rather than being flattened into a generic denial.
		if len(c.verdicts) > 0 && !errors.Is(err, corenet.RequestDenied) &&
			!errors.Is(err, corenet.UnsafePath) {
			t.Errorf("Allow = %v, want the member's own sentinel", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
