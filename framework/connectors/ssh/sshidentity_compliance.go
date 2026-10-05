package ssh

import (
	"github.com/kitsunium/sdk/framework/entitlement"
)

// Both assertions are compile-time only, and they fail for different reasons.
//
// The first catches a method added to the frozen port: it fails HERE rather than
// at a consumer, which is the earliest place this repository can notice.
//
// The second is the one that would otherwise be silent. The engine reaches
// BoundProver by TYPE ASSERTION, which cannot fail a build — an implementation
// that stopped satisfying it would quietly fall back to the unbound proof and
// nothing would say so. This line is what turns that into a compile error.
var (
	_ entitlement.Identity    = (*SSHIdentity)(nil)
	_ entitlement.BoundProver = (*SSHIdentity)(nil)
)
