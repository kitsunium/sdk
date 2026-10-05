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
// policy exists to permit. Read a verdict with [Decision].Granted, never with
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
// It is not an authenticator. The subject comes from pkg/v1/security/session (a
// revocable server-side session) or pkg/v1/security/token (a self-contained signed
// claim set); this package takes it as given and reads no header, mints
// nothing, and verifies no credential.
//
// It is not a policy language. A condition is a Go func, a grant table is a Go
// slice, and a resource is a string compared by equality — no expression
// grammar, no wildcards, no file format, no relationship tuples. What a DSL
// would buy is a deployment property, and it is available by loading your own
// rule data through pkg/v1/app/config and building the policy from it, in your
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
// code — each field read with its Key and StringValue methods, under the keys
// "outcome", "subject", "action", "resource" and "cause_code". Neither may go
// on the wire — see pkg/v1/errs. A caller that must distinguish an evaluation
// fault from a refusal for alerting calls the [Policy] itself and reads the
// (decision, error) pair; [Check] flattens them deliberately.
package authz
