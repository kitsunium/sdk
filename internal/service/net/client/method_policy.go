// Package client — the method allowlist policy.
package client

import (
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// methodPolicy admits a closed set of HTTP methods.
type methodPolicy struct {
	// allowed is the uppercase method set; an empty set admits nothing.
	allowed map[string]struct{}
}

// Allow implements corenet.Policy.
func (p *methodPolicy) Allow(req corenet.RequestValue) error {
	_, ok := p.allowed[strings.ToUpper(req.Method)]
	//: an empty or non-matching set refuses — the safe direction for an allowlist.
	if !ok {
		//: the method is named because it is not sensitive; the path is not.
		return errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
			errs.String("why", "method not allowed"), errs.String("method", req.Method))
	}
	//: the method is admitted; other policies still get their say.
	return nil
}
