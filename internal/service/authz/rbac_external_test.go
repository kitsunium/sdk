package authz_test

import (
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// editorGrants is the table every RBAC test evaluates against: one role, two
// permissions, one attribute name the caller chose.
func editorGrants() svcauthz.RBACConfig {
	//: RolesAttr is spelled out because the SDK has no default for it.
	return svcauthz.RBACConfig{
		RolesAttr: "roles",
		Grants: []svcauthz.GrantValue{{
			Role: "editor",
			Permissions: []svcauthz.PermissionValue{
				{Action: "publish", Resource: "article"},
				{Action: "read", Resource: "article"},
			},
		}},
	}
}

// TestRBACGrantsWhatARoleConfers is the happy path, and the only one in this
// file that reaches Allow.
func TestRBACGrantsWhatARoleConfers(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	request := coreauthz.NewRequestValue("u-1", "publish", "article",
		coreauthz.AttrStrings("roles", "reviewer", "editor"))
	decision, err := policy(t.Context(), request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != coreauthz.Allow {
		t.Fatalf("decision = %v, want allow", decision)
	}
}

// TestRBACAbstainsRatherThanDenying is the most consequential assertion in the
// package.
//
// An evaluator that refused where it merely had no grant would be absorbing
// under DenyOverrides: composed with anything else it would veto every request
// the other policy existed to permit. The test composes it with a policy that
// grants, and requires the grant to survive.
func TestRBACAbstainsRatherThanDenying(t *testing.T) {
	t.Parallel()
	rbac := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	request := coreauthz.NewRequestValue("u-1", "delete", "article",
		coreauthz.AttrStrings("roles", "editor"))
	decision, err := rbac(t.Context(), request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != coreauthz.Abstain {
		t.Fatalf("decision = %v, want abstain — an ungranted request is not a refusal", decision)
	}
	composed := svcauthz.DenyOverrides(rbac, constant(coreauthz.Allow))
	if err := svcauthz.Check(t.Context(), composed, request); err != nil {
		t.Fatalf("RBAC vetoed a grant from another policy: %v", err)
	}
}

// TestRBACAbstainsForARoleTheSubjectDoesNotHold covers the other abstention:
// the permission exists in the table, the subject simply does not hold the
// role that confers it.
func TestRBACAbstainsForARoleTheSubjectDoesNotHold(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	request := coreauthz.NewRequestValue("u-1", "publish", "article",
		coreauthz.AttrStrings("roles", "reader"))
	decision, err := policy(t.Context(), request)
	if err != nil || decision != coreauthz.Abstain {
		t.Fatalf("decision = %v err = %v, want abstain and no error", decision, err)
	}
}

// TestEmptyRoleSetAbstainsAndAbsentRolesRefuse is decision four applied to
// RBAC. "This subject holds no roles" is an answer; "nobody told us" is not.
func TestEmptyRoleSetAbstainsAndAbsentRolesRefuse(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	empty := coreauthz.NewRequestValue("anon", "publish", "article", coreauthz.AttrStrings("roles"))
	decision, err := policy(t.Context(), empty)
	if err != nil || decision != coreauthz.Abstain {
		t.Fatalf("empty role set: decision = %v err = %v, want abstain and no error", decision, err)
	}
	absent := coreauthz.NewRequestValue("anon", "publish", "article")
	decision, err = policy(t.Context(), absent)
	if decision != coreauthz.Deny {
		t.Fatalf("absent roles: decision = %v, want deny", decision)
	}
	if !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
		t.Fatalf("absent roles: err = %v, want ATTRIBUTE_MISSING", err)
	}
}

// TestAbsentRolesRefuseEvenForAnUngrantedPermission pins the evaluation ORDER.
// Checking the attribute after the grant lookup would make the same broken
// request refuse on one path and abstain on another — the fault would only
// show up on the requests that would have matched.
func TestAbsentRolesRefuseEvenForAnUngrantedPermission(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	request := coreauthz.NewRequestValue("anon", "delete", "unknown-thing")
	decision, err := policy(t.Context(), request)
	if decision != coreauthz.Deny || !errs.HasCode(err, coreauthz.CodeAttributeMissing) {
		t.Fatalf("decision = %v err = %v, want deny + ATTRIBUTE_MISSING regardless of the permission",
			decision, err)
	}
}

// TestRolesUnderTheWrongKindRefuse covers the producer bug that a string map
// would have hidden: the roles arriving as one comma-joined string.
func TestRolesUnderTheWrongKindRefuse(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	request := coreauthz.NewRequestValue("u-1", "publish", "article",
		coreauthz.AttrString("roles", "editor,reviewer"))
	decision, err := policy(t.Context(), request)
	if decision != coreauthz.Deny {
		t.Fatalf("decision = %v, want deny", decision)
	}
	if !errs.HasCode(err, coreauthz.CodeAttributeKindMismatch) {
		t.Fatalf("err = %v, want ATTRIBUTE_KIND_MISMATCH", err)
	}
}

// TestResourceMatchingIsByteEquality states the absence of any wildcard,
// prefix or separator the SDK would have to invent.
func TestResourceMatchingIsByteEquality(t *testing.T) {
	t.Parallel()
	policy := svcauthz.Must(svcauthz.NewRBAC(editorGrants()))
	tests := []struct {
		name     string
		resource string
	}{
		{"the star is a literal", "*"},
		{"a child path is another resource", "article/42"},
		{"case matters", "Article"},
		{"whitespace matters", "article "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := coreauthz.NewRequestValue("u-1", "publish", tc.resource,
				coreauthz.AttrStrings("roles", "editor"))
			decision, err := policy(t.Context(), request)
			if err != nil {
				t.Fatalf("policy: %v", err)
			}
			if decision != coreauthz.Abstain {
				t.Fatalf("decision = %v, want abstain — %q must not match %q",
					decision, tc.resource, "article")
			}
		})
	}
}

// TestGrantTableRefusalsRunAtConstruction covers every shape that could never
// grant correctly. Each fails once, at start-up, instead of silently on every
// request thereafter.
func TestGrantTableRefusalsRunAtConstruction(t *testing.T) {
	t.Parallel()
	valid := []svcauthz.PermissionValue{{Action: "read", Resource: "doc"}}
	tests := []struct {
		name string
		cfg  svcauthz.RBACConfig
	}{
		{"no roles attribute", svcauthz.RBACConfig{Grants: []svcauthz.GrantValue{{Role: "r", Permissions: valid}}}},
		{"no grants", svcauthz.RBACConfig{RolesAttr: "roles"}},
		{
			"unnamed role",
			svcauthz.RBACConfig{RolesAttr: "roles", Grants: []svcauthz.GrantValue{{Permissions: valid}}},
		},
		{
			"role that confers nothing",
			svcauthz.RBACConfig{RolesAttr: "roles", Grants: []svcauthz.GrantValue{{Role: "r"}}},
		},
		{
			"permission with an empty action",
			svcauthz.RBACConfig{RolesAttr: "roles", Grants: []svcauthz.GrantValue{{
				Role: "r", Permissions: []svcauthz.PermissionValue{{Resource: "doc"}},
			}}},
		},
		{
			"permission with an empty resource",
			svcauthz.RBACConfig{RolesAttr: "roles", Grants: []svcauthz.GrantValue{{
				Role: "r", Permissions: []svcauthz.PermissionValue{{Action: "read"}},
			}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy, err := svcauthz.NewRBAC(tc.cfg)
			if policy != nil {
				t.Fatal("a refused table still produced a policy")
			}
			if !errs.HasCode(err, svcauthz.CodeGrantInvalid) {
				t.Fatalf("err = %v, want GRANT_INVALID", err)
			}
		})
	}
}

// TestGrantsAreIsolatedFromLaterMutation pins that the grant table is READ at
// construction, not held by reference: a caller editing the slice it passed
// must not be able to add a permission to a policy that is already serving.
func TestGrantsAreIsolatedFromLaterMutation(t *testing.T) {
	t.Parallel()
	cfg := editorGrants()
	policy := svcauthz.Must(svcauthz.NewRBAC(cfg))
	cfg.Grants[0].Permissions[0] = svcauthz.PermissionValue{Action: "delete", Resource: "article"}
	request := coreauthz.NewRequestValue("u-1", "delete", "article",
		coreauthz.AttrStrings("roles", "editor"))
	decision, err := policy(t.Context(), request)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if decision != coreauthz.Abstain {
		t.Fatalf("decision = %v, want abstain — the live policy read a mutated grant", decision)
	}
}
