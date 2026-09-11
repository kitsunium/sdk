// Package authz — hosts the construction-time refusals.
//
// Every check in this file rejects a policy that could never answer correctly,
// at the moment it is assembled rather than on the request that trips over it.
// A policy is built once at start-up and evaluated on every request; a fault
// found here costs one process start, and the same fault found at evaluation
// time costs one silent misbehaviour per request until somebody notices.
package authz

import (
	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// validateRBAC refuses a grant table that could never grant correctly.
func validateRBAC(cfg RBACConfig) error {
	//: the attribute name has no default; guessing it is the SDK inventing
	//: the caller's vocabulary, and a wrong guess finds no roles ever.
	if cfg.RolesAttr == "" {
		//: name the field so the fix is one line at the call site.
		return grantFault("RolesAttr is empty", -1, "")
	}
	//: an empty table denies every request in the program, silently.
	if len(cfg.Grants) == 0 {
		//: refuse rather than fail closed without a word.
		return grantFault("Grants is empty", -1, "")
	}
	//: every row is checked; the first fault returns, so the caller fixes
	//: them one at a time rather than reading a truncated list.
	for index, grant := range cfg.Grants {
		//: per-grant checks, reported with the index so the row is findable.
		if err := validateGrant(index, grant); err != nil {
			//: first fault wins; the caller fixes them one at a time.
			return err
		}
	}
	//: every row can match something a request could ask for.
	return nil
}

// validateGrant refuses one row of the grant table.
func validateGrant(index int, grant GrantValue) error {
	//: an unnamed role can never appear in a roles attribute.
	if grant.Role == "" {
		//: name the row.
		return grantFault("Role is empty", index, "")
	}
	//: a role that confers nothing is a wiring mistake that fails closed.
	if len(grant.Permissions) == 0 {
		//: name the row and the role.
		return grantFault("Permissions is empty", index, grant.Role)
	}
	//: every permission the row confers must be able to match a request.
	for _, permission := range grant.Permissions {
		//: an empty half can only match a request with the same empty half,
		//: which is exactly the request a caller never meant to grant.
		if permission.Action == "" || permission.Resource == "" {
			//: name the row and the role.
			return grantFault("permission has an empty Action or Resource", index, grant.Role)
		}
	}
	//: the row is usable.
	return nil
}

// validateRules refuses a rule set that could never decide correctly.
func validateRules(rules []RuleValue) error {
	//: a rule set with no rules abstains on everything, silently.
	if len(rules) == 0 {
		//: refuse rather than build an evaluator that can only abstain.
		return ruleFault("no rules", -1, "")
	}
	//: every rule is checked; the first fault returns.
	for index, rule := range rules {
		//: per-rule checks, reported with the index and the rule's name.
		if err := validateRule(index, rule); err != nil {
			//: first fault wins.
			return err
		}
	}
	//: every rule can fire and knows what to do when it does.
	return nil
}

// validateRule refuses one attribute rule.
func validateRule(index int, rule RuleValue) error {
	//: the name is the only handle a private diagnostic has on a rule; it
	//: never reaches the wire, and without it a refusal is untraceable.
	if rule.Name == "" {
		//: name the position, since there is no name to give.
		return ruleFault("Name is empty", index, "")
	}
	//: an empty half matches only a request with the same empty half.
	if rule.Action == "" || rule.Resource == "" {
		//: name the rule.
		return ruleFault("Action or Resource is empty", index, rule.Name)
	}
	//: a rule with no condition would fire unconditionally — the caller has
	//: to write that predicate themselves so the grant is visible in the diff.
	if rule.When == nil {
		//: name the rule.
		return ruleFault("When is nil", index, rule.Name)
	}
	//: Abstain is the zero Decision, so an unset Effect lands here; a rule
	//: that abstains when it fires is a rule that does nothing.
	if rule.Effect != coreauthz.Allow && rule.Effect != coreauthz.Deny {
		//: name the rule and the value that was not an effect.
		return ruleFault("Effect is not Allow or Deny, got "+rule.Effect.String(), index, rule.Name)
	}
	//: the rule is usable.
	return nil
}

// grantFault builds the GrantInvalid refusal, naming the offending row.
func grantFault(detail string, index int, role string) error {
	//: origin-wins keeps the sentinel's identity; the position is diagnostic.
	return errs.Wrap(GrantInvalid, errs.WrapParams{},
		errs.String("detail", detail),
		errs.Int("grant_index", index),
		errs.String("role", role))
}

// ruleFault builds the RuleInvalid refusal, naming the offending rule.
func ruleFault(detail string, index int, name string) error {
	//: origin-wins keeps the sentinel's identity; the position is diagnostic.
	return errs.Wrap(RuleInvalid, errs.WrapParams{},
		errs.String("detail", detail),
		errs.Int("rule_index", index),
		errs.String("rule", name))
}

// conditionFault builds the ConditionInvalid refusal for a condition
// constructor that cannot honour its arguments.
func conditionFault(detail, attribute string) error {
	//: origin-wins keeps the sentinel's identity; the argument is diagnostic.
	return errs.Wrap(ConditionInvalid, errs.WrapParams{},
		errs.String("detail", detail),
		errs.String("attribute", attribute))
}

// kindName renders an AttrKind for a diagnostic field.
func kindName(kind coreauthz.AttrKind) string {
	//: switch rather than a table, so a new kind fails to compile silently.
	switch kind {
	//: a single text value.
	case coreauthz.KindString:
		//: a single text value.
		return "string"
	//: a whole number.
	case coreauthz.KindInt64:
		//: a whole number.
		return "int64"
	//: a flag.
	case coreauthz.KindBool:
		//: a flag.
		return "bool"
	//: an unordered set of text values.
	case coreauthz.KindStrings:
		//: an unordered set of text values.
		return "strings"
	//: the zero kind, which only reaches a diagnostic through an absent read.
	case coreauthz.KindInvalid:
		//: the zero kind; in a diagnostic it means the attribute was absent.
		return "absent"
	//: out of contract; it must not borrow one of the real kind names.
	default:
		//: out of contract — never rendered as one of the real kinds.
		return "unknown"
	}
}
