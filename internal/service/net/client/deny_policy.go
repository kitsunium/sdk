// Package client — the path denylist policy.
package client

import (
	"regexp"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// denyPolicy refuses paths matching any of its anchored patterns. It exists so a
// narrow exclusion can be carved out of a broader allow pattern without
// rewriting that pattern into something unreadable.
type denyPolicy struct {
	// patterns were anchored at construction.
	patterns []*regexp.Regexp
}

// Allow implements corenet.Policy.
func (p *denyPolicy) Allow(req corenet.RequestValue) error {
	err := checkPath(req.EscapedPath)
	//: dot segments would otherwise let a denied path be spelled around.
	if err != nil {
		//: surface UNSAFE_PATH unchanged.
		return err
	}
	//: any match is final; a deny is not overridable by an allow pattern.
	for _, re := range p.patterns {
		//: a whole-path match against an already-anchored pattern.
		if re.MatchString(req.EscapedPath) {
			//: refuse without echoing the path.
			return errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
				errs.String("why", "path explicitly denied"))
		}
	}
	//: nothing denied this request.
	return nil
}
