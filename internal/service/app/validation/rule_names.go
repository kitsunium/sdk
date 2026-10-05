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
