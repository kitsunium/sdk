//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/authz .

// Package authz is the public facade for the SDK's authorization domain: it
// answers "may this subject do this to that", and nothing else.
//
//	editor := authz.Must(authz.NewRBAC(authz.RBACConfig{
//	    RolesAttr: "roles", // no default — you name your own vocabulary
//	    Grants: []authz.Grant{{
//	        Role:        "editor",
//	        Permissions: []authz.Permission{{Action: "publish", Resource: "article"}},
//	    }},
//	}))
//
//	owner := authz.MustCondition(authz.AttrMatchesSubject("author"))
//	rules := authz.Must(authz.NewABAC(authz.Rule{
//	    Name: "locked-articles-are-frozen", Action: "publish", Resource: "article",
//	    Effect: authz.Deny, When: authz.MustCondition(authz.AttrIsTrue("locked")),
//	}, authz.Rule{
//	    Name: "authors-publish-their-own", Action: "publish", Resource: "article",
//	    Effect: authz.Allow, When: owner,
//	}))
//
//	policy := authz.DenyOverrides(editor, rules)
//
//	req := authz.NewRequest("u-42", "publish", "article",
//	    authz.AttrStrings("roles", "editor"),
//	    authz.AttrString("author", "u-42"),
//	    authz.AttrBool("locked", false))
//
//	if err := authz.Check(ctx, policy, req); err != nil {
//	    return err // errs.HasCode(err, authz.CodePermissionDenied)
//	}
//
// # Four decisions, all of them security properties
//
// **The default is refusal.** A request no policy has an opinion about is
// denied. [Check] is the one place that closes the world, and it closes toward
// [Deny]; there is no setting that opens it.
//
// **Refusal wins a conflict.** [DenyOverrides] is the only combining
// algorithm, and it is not configurable. Permit-overrides, first-applicable
// and only-one-applicable are refused by name: under deny-overrides a wrong
// rule set produces a request that should have been allowed and was not, which
// is reported within the hour; under permit-overrides it produces one that
// should have been refused and was not, which is reported by whoever exploits
// it.
//
// **Abstention is a real answer.** [Decision] has three states, not two.
// [NewRBAC] answers [Abstain] — never [Deny] — for a request it has no grant
// for, so composing it with another policy does not veto everything that other
// policy exists to permit. Read a verdict with [Decision.Granted], never with
// `!= Deny`, which is true for [Abstain].
//
// **An absent attribute is not a false one.** `department == "finance"` on a
// subject with no department is not a comparison that failed — it is one that
// never happened. Every built-in condition reports it as an error, every
// combinator treats that error as absorbing (including [Not], which propagates
// it instead of inverting it), and the rule's effect is irrelevant: the
// request is refused.
//
// # What this package is not
//
// It is not an authenticator. The subject comes from pkg/v1/session (a
// revocable server-side session) or pkg/v1/token (a self-contained signed
// claim set); this package takes it as given and reads no header, mints
// nothing, and verifies no credential.
//
// It is not a policy language. A condition is a Go func, a grant table is a Go
// slice, and a resource is a string compared by equality — no expression
// grammar, no wildcards, no file format, no relationship tuples. What a DSL
// would buy is a deployment property, and it is available by loading your own
// rule data through pkg/v1/config and building the policy from it, in your
// vocabulary rather than the SDK's.
//
// It is not a framework. A firewall, voters wired onto routes, a role
// catalogue, "what an admin is" and what a denied request looks like on the
// wire all belong one layer up. The SDK ships the evaluation.
//
// # The refusal tells the caller nothing
//
// Every refusal is [PermissionDenied], with the same code, the same HTTP 403
// and the same sentence — "Access to the requested resource is denied" —
// whether the request was explicitly denied, matched no rule at all, or could
// not be evaluated. A message that explained itself would be a description of
// the policy set handed to the party the policy exists to keep out: "you are
// not an admin" names the role model, "missing attribute department" names the
// next value to forge.
//
// The diagnosis is not lost, it is moved: errs.PrivateOf and errs.FieldsOf
// carry the outcome, the subject, the action, the resource and the underlying
// code. Neither may go on the wire — see pkg/v1/errs. A caller that must
// distinguish an evaluation fault from a refusal for alerting calls the
// [Policy] itself and reads the (decision, error) pair; [Check] flattens them
// deliberately.
package authz

import (
	"context"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	// Abstain means a policy has no opinion about this request. It is the
	// ZERO Decision, it grants nothing, and [Check] refuses it.
	Abstain Decision = coreauthz.Abstain
	// Allow means a policy permits the request. It is the only granting state,
	// and any [Deny] in the composition still outranks it.
	Allow Decision = coreauthz.Allow
	// Deny means a policy refuses the request. It is absorbing.
	Deny Decision = coreauthz.Deny

	// KindInvalid is the zero AttrKind. [NewRequest] drops an attribute
	// carrying it, so such an attribute is absent rather than unusable.
	KindInvalid AttrKind = coreauthz.KindInvalid
	// KindString is a single text value.
	KindString AttrKind = coreauthz.KindString
	// KindInt64 is a whole number — including a Unix timestamp.
	KindInt64 AttrKind = coreauthz.KindInt64
	// KindBool is a flag, which can be present and false.
	KindBool AttrKind = coreauthz.KindBool
	// KindStrings is an unordered set of text values — roles, groups, scopes.
	KindStrings AttrKind = coreauthz.KindStrings
)

const (
	// CodePermissionDenied identifies every refusal this domain produces —
	// explicit, by abstention, or by an evaluation that could not complete.
	CodePermissionDenied errs.Code = coreauthz.CodePermissionDenied
	// CodeAttributeMissing identifies a rule that named an attribute the
	// request does not carry. It is a failure, never a false comparison.
	CodeAttributeMissing errs.Code = coreauthz.CodeAttributeMissing
	// CodeAttributeKindMismatch identifies an attribute present under another
	// kind than the rule compares.
	CodeAttributeKindMismatch errs.Code = coreauthz.CodeAttributeKindMismatch
	// CodePolicyMisconfigured identifies a nil policy in a composition, or one
	// that returned a decision outside allow/deny/abstain.
	CodePolicyMisconfigured errs.Code = coreauthz.CodePolicyMisconfigured
	// CodeGrantInvalid identifies an RBAC grant table refused at construction.
	CodeGrantInvalid errs.Code = svcauthz.CodeGrantInvalid
	// CodeRuleInvalid identifies an ABAC rule set refused at construction.
	CodeRuleInvalid errs.Code = svcauthz.CodeRuleInvalid
	// CodeConditionInvalid identifies a condition constructor refused at
	// construction.
	CodeConditionInvalid errs.Code = svcauthz.CodeConditionInvalid
)

// Decision is the public alias for the three-valued verdict: [Allow], [Deny]
// or [Abstain]. Its zero value is [Abstain].
type Decision = coreauthz.Decision

// Policy is the public alias for the authorization port. It is a func type, so
// it can never grow a method and break a downstream implementer (ADR 0039).
type Policy = coreauthz.Policy

// Condition is the public alias for an attribute predicate. Returning an error
// means "could not be evaluated", which is absorbing everywhere.
type Condition = coreauthz.Condition

// Request is the public alias for the immutable question a policy answers.
type Request = coreauthz.RequestValue

// Attr is the public alias for one typed, named fact attached to a [Request].
type Attr = coreauthz.AttrValue

// AttrKind is the public alias for the type an [Attr] carries. The kind is
// part of the attribute's identity: a text rule against a number MISMATCHES,
// it does not compare false.
type AttrKind = coreauthz.AttrKind

// Permission is the public alias for one (action, resource) pair a role
// confers. Resource is the KIND of thing; instance facts belong in attributes.
type Permission = svcauthz.PermissionValue

// Grant is the public alias for one role and the permissions it confers.
type Grant = svcauthz.GrantValue

// RBACConfig is the public alias for [NewRBAC]'s parameters.
type RBACConfig = svcauthz.RBACConfig

// Rule is the public alias for one attribute rule: what it is about, what it
// decides, and when it fires.
type Rule = svcauthz.RuleValue

var (
	// PermissionDenied is the one refusal. Every non-granting outcome converts
	// to it, with the same code, status and sentence.
	PermissionDenied = coreauthz.PermissionDenied
	// AttributeMissing is reported by a condition whose rule named an
	// attribute the request does not carry.
	AttributeMissing = coreauthz.AttributeMissing
	// AttributeKindMismatch is reported by a condition whose attribute is
	// present under another kind.
	AttributeKindMismatch = coreauthz.AttributeKindMismatch
	// PolicyMisconfigured is reported for a nil policy in a composition, or a
	// decision outside allow/deny/abstain.
	PolicyMisconfigured = coreauthz.PolicyMisconfigured
	// GrantInvalid is returned by [NewRBAC] for a grant table it cannot honour.
	GrantInvalid = svcauthz.GrantInvalid
	// RuleInvalid is returned by [NewABAC] for a rule set it cannot honour.
	RuleInvalid = svcauthz.RuleInvalid
	// ConditionInvalid is returned by a condition constructor for arguments it
	// cannot honour.
	ConditionInvalid = svcauthz.ConditionInvalid
)

// NewRequest builds the immutable question. Attributes are indexed by key,
// last one wins, and one with an empty key or the zero kind is dropped — so
// every attribute a rule can find is one it can use.
func NewRequest(subject, action, resource string, attrs ...Attr) Request {
	//: delegate to the core constructor; the alias makes it the same type.
	return coreauthz.NewRequestValue(subject, action, resource, attrs...)
}

// AttrString builds a text attribute.
func AttrString(key, value string) Attr {
	//: delegate to the core constructor.
	return coreauthz.AttrString(key, value)
}

// AttrInt64 builds a whole-number attribute. Times travel as Unix seconds.
func AttrInt64(key string, value int64) Attr {
	//: delegate to the core constructor.
	return coreauthz.AttrInt64(key, value)
}

// AttrBool builds a flag attribute. Present-and-false is a different fact from
// absent, and this is how the first one is stated.
func AttrBool(key string, value bool) Attr {
	//: delegate to the core constructor.
	return coreauthz.AttrBool(key, value)
}

// AttrStrings builds a set attribute — roles, groups, scopes. Passing no
// values states that the subject holds none, which is NOT the same as omitting
// the attribute: the first abstains, the second refuses.
func AttrStrings(key string, values ...string) Attr {
	//: delegate to the core constructor; the slice is cloned there.
	return coreauthz.AttrStrings(key, values...)
}

// Check evaluates policy and reports the verdict as an error: nil when
// permitted, [PermissionDenied] otherwise. It is the closure — the one place
// where "nobody said Allow" becomes a refusal — and a nil policy refuses.
func Check(ctx context.Context, policy Policy, request Request) error {
	//: delegate to the service closure.
	return svcauthz.Check(ctx, policy, request)
}

// DenyOverrides composes policies: a [Deny] from any member wins, an [Allow]
// requires at least one grant and no refusal, and everything else abstains.
// Every member is evaluated — only a refusal short-circuits — so the result
// does not depend on the order the policies were listed in.
func DenyOverrides(policies ...Policy) Policy {
	//: delegate to the service combiner.
	return svcauthz.DenyOverrides(policies...)
}

// NewRBAC builds a role-based policy over a fixed grant table. It answers
// [Allow] or [Abstain] and never [Deny] for an ungranted request — see the
// package docs for why that is what makes it composable.
func NewRBAC(cfg RBACConfig) (policy Policy, err error) {
	//: delegate to the service constructor.
	return svcauthz.NewRBAC(cfg)
}

// NewABAC builds an attribute-based policy over a fixed rule set, folded by
// the same deny-overrides algorithm the policy set is.
func NewABAC(rules ...Rule) (policy Policy, err error) {
	//: delegate to the service constructor.
	return svcauthz.NewABAC(rules...)
}

// AttrEquals holds when the request carries key as text equal to want, and
// FAILS when the attribute is absent or is not text.
func AttrEquals(key, want string) (condition Condition, err error) {
	//: delegate to the service condition.
	return svcauthz.AttrEquals(key, want)
}

// AttrIsTrue holds when the request carries key as a flag that is set. Present
// and false does not hold; absent fails.
func AttrIsTrue(key string) (condition Condition, err error) {
	//: delegate to the service condition.
	return svcauthz.AttrIsTrue(key)
}

// AttrAtLeast holds when the request carries key as a number at least lo.
func AttrAtLeast(key string, lo int64) (condition Condition, err error) {
	//: delegate to the service condition.
	return svcauthz.AttrAtLeast(key, lo)
}

// AttrContains holds when the request carries key as a set containing want.
func AttrContains(key, want string) (condition Condition, err error) {
	//: delegate to the service condition.
	return svcauthz.AttrContains(key, want)
}

// AttrMatchesSubject holds when the request carries key as text equal to the
// subject — the ownership rule. An anonymous request owns nothing, so an empty
// subject never matches, not even an empty owner.
func AttrMatchesSubject(key string) (condition Condition, err error) {
	//: delegate to the service condition.
	return svcauthz.AttrMatchesSubject(key)
}

// Not inverts a condition's answer and PROPAGATES its failure unchanged. An
// attribute the request does not carry therefore stays unevaluable instead of
// becoming "true", which is the single sharpest form of the absent-attribute
// trap.
func Not(inner Condition) (condition Condition, err error) {
	//: delegate to the service combinator.
	return svcauthz.Not(inner)
}

// AllOf holds when every condition holds. Every branch is evaluated, so the
// answer is order-independent and a failure anywhere is reported.
func AllOf(conditions ...Condition) (condition Condition, err error) {
	//: delegate to the service combinator.
	return svcauthz.AllOf(conditions...)
}

// AnyOf holds when at least one condition holds — but an unevaluable branch is
// still absorbing, so a request cannot satisfy the rule by omitting the
// attribute one of its branches names.
func AnyOf(conditions ...Condition) (condition Condition, err error) {
	//: delegate to the service combinator.
	return svcauthz.AnyOf(conditions...)
}

// Must unwraps a policy constructor for a package-level var, panicking on
// error. Use it on wiring written in code, never on data read at run time.
func Must(policy Policy, err error) Policy {
	//: delegate to the service helper.
	return svcauthz.Must(policy, err)
}

// MustCondition unwraps a condition constructor for a package-level var,
// panicking on error. Same contract as [Must].
func MustCondition(condition Condition, err error) Condition {
	//: delegate to the service helper.
	return svcauthz.MustCondition(condition, err)
}
