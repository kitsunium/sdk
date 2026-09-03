// Package client — the path allowlist policy.
package client

import (
	"regexp"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pathPolicy admits paths matching any of its anchored patterns. A pathPolicy
// with no patterns admits nothing: an allowlist that grew empty by accident must
// close, never open.
type pathPolicy struct {
	// patterns were anchored at construction, so a caller cannot forget to.
	patterns []*regexp.Regexp
}

// Allow implements corenet.Policy.
func (p *pathPolicy) Allow(req corenet.RequestValue) error {
	err := checkPath(req.EscapedPath)
	//: dot segments and relative paths are refused before any pattern is tried.
	if err != nil {
		//: surface UNSAFE_PATH unchanged.
		return err
	}
	//: first matching pattern admits the request.
	for _, re := range p.patterns {
		//: the patterns are already anchored, so a match is a whole-path match.
		if re.MatchString(req.EscapedPath) {
			//: admitted.
			return nil
		}
	}
	//: no pattern covered the path — refuse without echoing it.
	return errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
		errs.String("why", "path not in the allow list"))
}
