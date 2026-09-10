package authz_test

import (
	"context"
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// Result sinks. Assigning into a package-level var keeps the measured call
// from being folded away AND keeps every error the benchmark produces
// reachable, so nothing is discarded into a blank identifier.
var (
	sinkRequest  coreauthz.RequestValue
	sinkDecision coreauthz.Decision
	sinkErr      error
)

// benchGrants is a table of a size a real service reaches: eight roles, four
// permissions each. The inverted index makes evaluation independent of it, and
// the benchmark exists to show that rather than assert it.
func benchGrants() svcauthz.RBACConfig {
	//: roles × resources, so the table has 32 entries over 8 permissions.
	roles := []string{"reader", "editor", "reviewer", "publisher", "admin", "auditor", "billing", "support"}
	resources := []string{"article", "comment", "invoice", "ticket"}
	grants := make([]svcauthz.GrantValue, 0, len(roles))
	for _, role := range roles {
		permissions := make([]svcauthz.PermissionValue, 0, len(resources))
		for _, resource := range resources {
			permissions = append(permissions, svcauthz.PermissionValue{Action: "read", Resource: resource})
		}
		grants = append(grants, svcauthz.GrantValue{Role: role, Permissions: permissions})
	}
	//: the last role also gets the permission the Allow benchmarks ask for.
	grants[len(grants)-1].Permissions = append(grants[len(grants)-1].Permissions,
		svcauthz.PermissionValue{Action: "publish", Resource: "article"})
	return svcauthz.RBACConfig{RolesAttr: "roles", Grants: grants}
}

// benchRequest is the request every evaluation benchmark answers.
func benchRequest(action string) coreauthz.RequestValue {
	//: five attributes, one of each kind the built-in conditions read.
	return coreauthz.NewRequestValue("u-42", action, "article",
		coreauthz.AttrStrings("roles", "reader", "support"),
		coreauthz.AttrString("author", "u-42"),
		coreauthz.AttrBool("locked", false),
		coreauthz.AttrInt64("level", 3))
}

// BenchmarkNewRequest measures building the question — the one allocation a
// caller pays per request before any policy runs.
func BenchmarkNewRequest(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		//: the whole cost of the question: one map plus the value itself.
		sinkRequest = benchRequest("publish")
	}
}

// BenchmarkRBACAllow measures a grant hit: one map lookup plus a scan of the
// roles that confer exactly that permission.
func BenchmarkRBACAllow(b *testing.B) {
	policy := svcauthz.Must(svcauthz.NewRBAC(benchGrants()))
	request := benchRequest("publish")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: one map lookup plus a scan of the roles conferring it.
		sinkDecision, sinkErr = policy(ctx, request)
	}
	//: read once, outside the timed loop: an error here would mean the
	//: benchmark measured the refusal path instead of the one it names.
	requireNoBenchErr(b)
}

// BenchmarkRBACAbstain measures the far more common case: a permission no held
// role confers. It is one map lookup and no scan at all.
func BenchmarkRBACAbstain(b *testing.B) {
	policy := svcauthz.Must(svcauthz.NewRBAC(benchGrants()))
	request := benchRequest("delete")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: one map lookup that misses; no role scan at all.
		sinkDecision, sinkErr = policy(ctx, request)
	}
	//: read once, outside the timed loop.
	requireNoBenchErr(b)
}

// BenchmarkABACAllow measures a two-rule set where both rules match the
// request and one of them fires.
func BenchmarkABACAllow(b *testing.B) {
	policy := svcauthz.Must(svcauthz.NewABAC(
		svcauthz.RuleValue{
			Name: "locked", Action: "publish", Resource: "article", Effect: coreauthz.Deny,
			When: svcauthz.MustCondition(svcauthz.AttrIsTrue("locked")),
		},
		svcauthz.RuleValue{
			Name: "owner", Action: "publish", Resource: "article", Effect: coreauthz.Allow,
			When: svcauthz.MustCondition(svcauthz.AttrMatchesSubject("author")),
		},
	))
	request := benchRequest("publish")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: both rules match; the first declines, the second fires.
		sinkDecision, sinkErr = policy(ctx, request)
	}
	//: read once, outside the timed loop.
	requireNoBenchErr(b)
}

// BenchmarkCheckAllow measures the whole permitted path: the composition of an
// RBAC and an ABAC policy, through the closure. This is what a request pays.
func BenchmarkCheckAllow(b *testing.B) {
	policy := benchComposed()
	request := benchRequest("publish")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: the whole permitted path, closure included.
		sinkErr = svcauthz.Check(ctx, policy, request)
	}
	//: read once, outside the timed loop.
	requireNoBenchErr(b)
}

// BenchmarkCheckDenied measures the refusal path, which builds the error and
// its diagnostic fields. It is deliberately reported next to the permitted one
// so the cost of refusing is visible rather than assumed to be free.
func BenchmarkCheckDenied(b *testing.B) {
	policy := benchComposed()
	request := benchRequest("delete")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: the refusal path — this one is EXPECTED to be non-nil, which is
		//: why it is asserted the other way round below.
		sinkErr = svcauthz.Check(ctx, policy, request)
	}
	//: the inverse assertion: a nil here would mean the benchmark measured a
	//: grant and the number below would be the wrong path's.
	if sinkErr == nil {
		b.Fatal("the denied benchmark permitted the request")
	}
}

// BenchmarkPerRequest measures what ONE HTTP request actually pays: building
// the question and then answering it. The two halves are benchmarked
// separately above because they have different shapes — construction
// allocates, evaluation does not — but a service never pays one without the
// other, and a change that trades one against the other is only honest if this
// number is quoted beside them.
func BenchmarkPerRequest(b *testing.B) {
	policy := benchComposed()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		//: the whole per-request cost: the question, then the verdict.
		sinkErr = svcauthz.Check(ctx, policy, benchRequest("publish"))
	}
	//: read once, outside the timed loop.
	requireNoBenchErr(b)
}

// requireNoBenchErr fails the benchmark when the last iteration produced an
// error. It runs after the timed loop, so it costs the measurement nothing.
func requireNoBenchErr(b *testing.B) {
	b.Helper()
	//: an error means the benchmark measured a path other than the one it names.
	if sinkErr != nil {
		b.Fatalf("benchmark produced an error: %v", sinkErr)
	}
}

// benchComposed builds the RBAC + ABAC composition the Check benchmarks use.
func benchComposed() coreauthz.Policy {
	//: the shape a service actually wires: coarse grants, fine rules.
	rbac := svcauthz.Must(svcauthz.NewRBAC(benchGrants()))
	abac := svcauthz.Must(svcauthz.NewABAC(
		svcauthz.RuleValue{
			Name: "locked", Action: "publish", Resource: "article", Effect: coreauthz.Deny,
			When: svcauthz.MustCondition(svcauthz.AttrIsTrue("locked")),
		},
		svcauthz.RuleValue{
			Name: "owner", Action: "publish", Resource: "article", Effect: coreauthz.Allow,
			When: svcauthz.MustCondition(svcauthz.AttrMatchesSubject("author")),
		},
	))
	return svcauthz.DenyOverrides(rbac, abac)
}
