// Package client — the policy conjunction.
package client

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// conjunction requires every member policy to allow the request. Composing by
// conjunction rather than disjunction is deliberate: adding a policy can only
// ever narrow what is permitted, so a reviewer never has to check whether a new
// rule accidentally widened the surface.
type conjunction struct {
	// members must all allow; an empty set refuses.
	members []corenet.Policy
}

// Allow implements corenet.Policy.
func (p *conjunction) Allow(req corenet.RequestValue) error {
	//: an empty conjunction would admit everything, which is never what a caller
	//: assembling an allowlist means. Refuse instead of opening by default.
	if len(p.members) == 0 {
		//: no policy configured is a configuration error, not permission.
		return errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
			errs.String("why", "no policy configured"))
	}
	//: every member must agree.
	for _, m := range p.members {
		err := m.Allow(req)
		//: the first refusal wins and is returned unchanged.
		if err != nil {
			//: preserve the member's own reason.
			return err
		}
	}
	//: every member admitted the request.
	return nil
}
