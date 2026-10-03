// Package authz — hosts RBACConfig, the arguments NewRBAC is built from.
package authz

// RBACConfig configures [NewRBAC]: which request attribute carries the
// subject's roles, and what each role confers.
//
// Neither member has a default and both are refused when empty, so a policy
// this package hands back can always answer the question it was built for. A
// grant table is data the CALLER writes — there is no file format for it and
// no parser, because a rule set is a Go value in this domain (ADR 0057 §D1).
type RBACConfig struct {
	// RolesAttr names the request attribute carrying the subject's roles, as
	// a [coreauthz.KindStrings] value.
	//
	// It has NO default, and an empty one is refused. Defaulting it to "roles"
	// would be the SDK inventing the one piece of vocabulary it has no way to
	// know — the application's — and a caller who spelled it "groups" would
	// get an evaluator that silently found no roles on every request. Naming
	// it is one line; guessing it wrong is an outage or a hole depending on
	// which way the mistake falls.
	RolesAttr string
	// Grants is the role → permissions table. An empty table is refused.
	Grants []GrantValue
}
