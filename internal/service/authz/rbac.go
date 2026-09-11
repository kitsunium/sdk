// Package authz — hosts the role-based evaluator.
package authz

import (
	"context"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// PermissionValue is one (action, resource) pair a role confers. Both halves
// are compared by byte equality: there is no wildcard, no prefix and no
// separator the SDK knows about.
type PermissionValue struct {
	// Action is the verb, spelled exactly as the request will spell it.
	Action string
	// Resource is the KIND of thing — "article", not "article/42". Instance
	// facts belong in the request's attributes, where an ABAC rule reads them.
	Resource string
}

// NewRBAC builds a role-based [coreauthz.Policy] over a fixed grant table.
//
// # It returns Allow or Abstain, and almost never Deny
//
// A request whose (action, resource) no held role confers yields
// [coreauthz.Abstain], NOT [coreauthz.Deny]. This is the single most
// consequential line in the package.
//
// An evaluator that refused where it merely had no grant would be absorbing
// under [DenyOverrides]: composed with anything else, it would veto every
// request the other policies existed to permit, and the usual repair — making
// the combiner permissive — reintroduces the hole one layer up. Abstaining
// keeps the closed-world default where it belongs: in [Check], once, for the
// whole composition.
//
// The two cases where it does refuse are not decisions about the request. They
// are the request being unanswerable: the roles attribute is absent
// ([coreauthz.AttributeMissing]) or carries another kind
// ([coreauthz.AttributeKindMismatch]).
//
// # An absent roles attribute is not an empty role set
//
// AttrStrings(key) with no values says "this subject holds no roles" and
// abstains, which is right for an anonymous request. Omitting the attribute
// says nothing at all, and is refused. The distinction costs the caller one
// always-set attribute and closes the case where a producer bug drops the
// roles and every "deny unless role X" rule in the system stops firing.
//
// The attribute is checked BEFORE the grant table is consulted, so the fault
// surfaces on every request rather than only on the ones that would have
// matched a grant.
func NewRBAC(cfg RBACConfig) (policy coreauthz.Policy, err error) {
	//: refuse a table that could never answer before building anything.
	if err := validateRBAC(cfg); err != nil {
		//: a construction fault is permanent; the caller fixes code, not input.
		return nil, err
	}
	//: index permission → conferring roles, so evaluation is one map lookup
	//: plus a scan of the (short) list of roles that confer that permission.
	holders := indexGrants(cfg.Grants)
	//: capture the attribute name once; the closure holds no mutable state.
	rolesAttr := cfg.RolesAttr
	//: the returned closure IS the Policy; it reads the index and nothing else.
	return func(_ context.Context, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
		//: evaluation needs no context: the table is in memory and does no I/O.
		return evaluateRBAC(holders, rolesAttr, request)
	}, nil
}

// evaluateRBAC answers one request against the indexed grant table.
func evaluateRBAC(holders map[PermissionValue][]string, rolesAttr string, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
	//: the roles attribute is checked first, so a producer bug is visible on
	//: every request and not only on the ones a grant would have matched.
	attr, ok := request.Attr(rolesAttr)
	//: absent is unanswerable — it is NOT an empty role set.
	if !ok {
		//: unanswerable, not unpermitted — refuse and name the attribute.
		return coreauthz.Deny, attrFault(coreauthz.AttributeMissing, rolesAttr, coreauthz.KindStrings, coreauthz.KindInvalid)
	}
	//: present but the wrong kind is the same failure wearing another hat.
	if attr.Kind() != coreauthz.KindStrings {
		//: refuse and report both kinds so the producer can be fixed.
		return coreauthz.Deny, attrFault(coreauthz.AttributeKindMismatch, rolesAttr, coreauthz.KindStrings, attr.Kind())
	}
	//: one lookup for the permission the request is asking about.
	roles := holders[PermissionValue{Action: request.Action(), Resource: request.Resource()}]
	//: scan only the roles that confer THIS permission, not every role held.
	for _, role := range roles {
		//: Contains scans the attribute in place — no clone on the hot path.
		if held, _ := attr.Contains(role); held {
			//: one held role that confers it is enough.
			return coreauthz.Allow, nil
		}
	}
	//: no grant matched — this evaluator has nothing to say, it does not refuse.
	return coreauthz.Abstain, nil
}

// attrFault builds an attribute-shaped refusal with the diagnostic fields an
// operator needs to fix the producing side.
func attrFault(sentinel *errs.Error, key string, want, got coreauthz.AttrKind) error {
	//: origin-wins keeps the sentinel's code and its single public sentence.
	return errs.Wrap(sentinel, errs.WrapParams{},
		errs.String("attribute", key),
		errs.String("want_kind", kindName(want)),
		errs.String("got_kind", kindName(got)))
}

// indexGrants inverts the role → permissions table into permission → roles.
//
// The inversion is what keeps evaluation independent of the table's size: a
// request costs one map lookup plus a scan of the roles that confer exactly
// that permission, which is one or two in every realistic table, rather than a
// walk of every role the subject holds against every grant.
func indexGrants(grants []GrantValue) map[PermissionValue][]string {
	//: size to the grant count; duplicate permissions only over-allocate.
	holders := make(map[PermissionValue][]string, len(grants))
	//: one pass over the table, inverting it as it goes.
	for _, grant := range grants {
		//: every permission the row confers gains this role as a holder.
		for _, permission := range grant.Permissions {
			//: append keeps declaration order, so a diagnostic is reproducible.
			holders[permission] = append(holders[permission], grant.Role)
		}
	}
	//: the map is never written again; the closure only reads it.
	return holders
}
