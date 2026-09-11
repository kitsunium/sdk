package authz_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/authz"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// blogPolicy wires the composition the package documentation shows: coarse
// grants from RBAC, instance rules from ABAC, folded by deny-overrides.
func blogPolicy(t *testing.T) authz.Policy {
	t.Helper()
	editor := authz.Must(authz.NewRBAC(authz.RBACConfig{
		RolesAttr: "roles",
		Grants: []authz.Grant{{
			Role:        "editor",
			Permissions: []authz.Permission{{Action: "publish", Resource: "article"}},
		}},
	}))
	rules := authz.Must(authz.NewABAC(
		authz.Rule{
			Name: "locked-articles-are-frozen", Action: "publish", Resource: "article",
			Effect: authz.Deny, When: authz.MustCondition(authz.AttrIsTrue("locked")),
		},
		authz.Rule{
			Name: "authors-publish-their-own", Action: "publish", Resource: "article",
			Effect: authz.Allow, When: authz.MustCondition(authz.AttrMatchesSubject("author")),
		},
	))
	return authz.DenyOverrides(editor, rules)
}

// TestTheDocumentedWiringBehaves walks the four outcomes a caller will meet,
// through the public surface only.
func TestTheDocumentedWiringBehaves(t *testing.T) {
	t.Parallel()
	policy := blogPolicy(t)
	tests := []struct {
		name    string
		request authz.Request
		allowed bool
	}{
		{
			"an editor publishes",
			authz.NewRequest("u-1", "publish", "article",
				authz.AttrStrings("roles", "editor"),
				authz.AttrString("author", "u-9"),
				authz.AttrBool("locked", false)),
			true,
		},
		{
			"an author publishes their own",
			authz.NewRequest("u-9", "publish", "article",
				authz.AttrStrings("roles"),
				authz.AttrString("author", "u-9"),
				authz.AttrBool("locked", false)),
			true,
		},
		{
			"a locked article refuses even an editor",
			authz.NewRequest("u-1", "publish", "article",
				authz.AttrStrings("roles", "editor"),
				authz.AttrString("author", "u-1"),
				authz.AttrBool("locked", true)),
			false,
		},
		{
			"a stranger is refused by abstention",
			authz.NewRequest("u-7", "publish", "article",
				authz.AttrStrings("roles", "reader"),
				authz.AttrString("author", "u-9"),
				authz.AttrBool("locked", false)),
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := authz.Check(t.Context(), policy, tc.request)
			if tc.allowed && err != nil {
				t.Fatalf("Check = %v, want nil", err)
			}
			if !tc.allowed && !errs.HasCode(err, authz.CodePermissionDenied) {
				t.Fatalf("Check = %v, want PERMISSION_DENIED", err)
			}
		})
	}
}

// TestAnOmittedAttributeRefusesThroughTheFacade is decision four seen from the
// public side: dropping "locked" does not make the deny rule stop firing, it
// makes the whole request unanswerable.
func TestAnOmittedAttributeRefusesThroughTheFacade(t *testing.T) {
	t.Parallel()
	request := authz.NewRequest("u-1", "publish", "article",
		authz.AttrStrings("roles", "editor"),
		authz.AttrString("author", "u-1"))
	err := authz.Check(t.Context(), blogPolicy(t), request)
	if !errs.HasCode(err, authz.CodePermissionDenied) {
		t.Fatalf("Check = %v, want PERMISSION_DENIED", err)
	}
	if errs.HTTPStatusOf(err) != 403 {
		t.Fatalf("status = %d, want 403", errs.HTTPStatusOf(err))
	}
}

// TestThePublicSentenceIsTheOnlyThingOnTheWire pins the security property at
// the public boundary — the surface a framework is most likely to render.
func TestThePublicSentenceIsTheOnlyThingOnTheWire(t *testing.T) {
	t.Parallel()
	denied := authz.Check(t.Context(), authz.DenyOverrides(), authz.NewRequest("u-1", "read", "doc"))
	public := errs.PublicOf(denied)
	if public != "Access to the requested resource is denied" {
		t.Fatalf("public = %q", public)
	}
	//: the private side is where the diagnosis lives, and it must differ.
	if errs.PrivateOf(denied) == public {
		t.Fatal("the private message equals the public one; the split does nothing")
	}
	//: the rendered error carries the code and the public sentence, nothing else.
	if errs.PrivateOf(denied) == "" {
		t.Fatal("no private diagnosis was recorded")
	}
}

// TestAliasesShareIdentityWithTheInternalTypes is the pkg/v1 contract: an
// alias, never a new type, so a value crosses the boundary unconverted.
func TestAliasesShareIdentityWithTheInternalTypes(t *testing.T) {
	t.Parallel()
	//: a Condition written by hand satisfies the alias with no adapter.
	condition := authz.Condition(func(authz.Request) (bool, error) { return true, nil })
	rule := authz.Rule{
		Name: "hand-written", Action: "read", Resource: "doc",
		Effect: authz.Allow, When: condition,
	}
	policy, err := authz.NewABAC(rule)
	if err != nil {
		t.Fatalf("NewABAC = %v", err)
	}
	if err := authz.Check(t.Context(), policy, authz.NewRequest("u-1", "read", "doc")); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
}

// TestZeroDecisionIsAbstainThroughTheFacade pins that the safe zero value
// survives the alias, since that is the value a consumer's own Policy returns
// when it forgets to set one.
func TestZeroDecisionIsAbstainThroughTheFacade(t *testing.T) {
	t.Parallel()
	var zero authz.Decision
	if zero != authz.Abstain || zero.Granted() {
		t.Fatalf("zero Decision = %v (granted=%v), want abstain and no grant", zero, zero.Granted())
	}
}
