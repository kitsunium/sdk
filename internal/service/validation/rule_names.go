// Package validation — the closed set of built-in rule names. A rule name is
// part of the published contract: it is what a ViolationValue.Rule carries,
// what an application keys its own translated messages on, and what a validate
// struct tag spells. Adding one is a decision the SDK maintains forever.
package validation

const (
	// ruleRequired names the presence rule.
	ruleRequired string = "required"
	// ruleMin names the lower numeric bound.
	ruleMin string = "min"
	// ruleMax names the upper numeric bound.
	ruleMax string = "max"
	// ruleBetween names the closed numeric interval.
	ruleBetween string = "between"
	// ruleLength names the rune-count rule on a string.
	ruleLength string = "length"
	// ruleCount names the element-count rule on a slice or array.
	ruleCount string = "count"
	// ruleOneOf names set membership.
	ruleOneOf string = "one_of"
	// rulePattern names the regular-expression rule.
	rulePattern string = "pattern"
)
