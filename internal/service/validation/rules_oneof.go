// Package validation — the tag half of set membership.
package validation

import (
	"reflect"
	"strconv"
	"strings"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// buildOneOf compiles oneof=a|b|c against a string or integer field.
func buildOneOf(name string, typ reflect.Type, arg string, hasArg bool) (check fieldCheck, err error) {
	//: an empty set is satisfied by nothing — the ADR 0031 trap, in a tag.
	if !hasArg || arg == "" {
		//: show the shape that works.
		return nil, rejectRule(name, tagOneOf, "the rule needs a '|'-separated list, as in oneof=red|green|blue")
	}
	//: split and trim; a stray space around a value is formatting.
	values := splitAllowed(arg)
	//: a list of nothing but separators is the same empty set.
	if len(values) == 0 {
		//: name the clause.
		return nil, rejectRule(name, tagOneOf, "the allowed set is empty")
	}
	//: dispatch on the field's kind, once.
	switch typ.Kind() {
	//: strings compare verbatim.
	case reflect.String:
		//: string membership.
		return stringOneOf(values), nil
	//: signed integers: every entry is parsed at compile time.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: signed membership.
		return signedOneOf(name, values)
	//: unsigned integers, same shape.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		//: unsigned membership.
		return unsignedOneOf(name, values)
	//: anything else, refused by name.
	default:
		//: a float set would compare by exact equality, which is a trap; a
		//: struct or slice set has no tag rendering. Refuse both by name.
		return nil, rejectRule(name, tagOneOf,
			"oneof requires a string or integer field; this field is a "+typ.Kind().String())
	}
}

// splitAllowed splits and trims the allowed list, dropping empty entries.
func splitAllowed(arg string) []string {
	//: split on the inner separator.
	parts := strings.Split(arg, oneOfSeparator)
	//: exact upper bound on capacity.
	values := make([]string, 0, len(parts))
	//: declaration order is the order the message enumerates.
	for _, part := range parts {
		//: surrounding space is formatting, not meaning.
		trimmed := strings.TrimSpace(part)
		//: an empty entry denotes nothing.
		if trimmed == "" {
			//: skip it.
			continue
		}
		//: contribute.
		values = append(values, trimmed)
	}
	//: the allowed values, as written.
	return values
}

// stringOneOf compiles membership over a string field.
func stringOneOf(values []string) fieldCheck {
	//: build the lookup once — membership is then one map probe.
	set := make(map[string]struct{}, len(values))
	//: duplicates collapse.
	for _, value := range values {
		//: presence is all the map records.
		set[value] = struct{}{}
	}
	//: message built once, echoing the schema only.
	message := oneOfMessage(values)
	//: one map probe per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: membership.
		if _, ok := set[fieldValue.String()]; ok {
			//: accepted.
			return nil
		}
		//: outside the set.
		return one(path, ruleOneOf, message, CodeNotInSet)
	}
}

// signedOneOf compiles membership over a signed integer field.
func signedOneOf(name string, values []string) (check fieldCheck, err error) {
	//: parse every entry at compile time; one bad entry refuses the rule
	//: rather than silently shrinking the set.
	set := make(map[int64]struct{}, len(values))
	//: keep the parsed values for the message, in declaration order.
	parsed := make([]int64, 0, len(values))
	//: parse.
	for _, value := range values {
		//: base 10, 64-bit.
		number, parseErr := strconv.ParseInt(value, decimalBase, bitSize64)
		//: refuse.
		if parseErr != nil {
			//: name the entry that failed.
			return nil, rejectRule(name, tagOneOf, "the entry "+clip(value)+" is not a whole number")
		}
		//: record.
		set[number] = struct{}{}
		//: enumerate.
		parsed = append(parsed, number)
	}
	//: message built once.
	message := oneOfMessage(parsed)
	//: one map probe per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: membership.
		if _, ok := set[fieldValue.Int()]; ok {
			//: accepted.
			return nil
		}
		//: outside the set.
		return one(path, ruleOneOf, message, CodeNotInSet)
	}, nil
}

// unsignedOneOf compiles membership over an unsigned integer field.
func unsignedOneOf(name string, values []string) (check fieldCheck, err error) {
	//: same shape as signedOneOf, over the unsigned accessor.
	set := make(map[uint64]struct{}, len(values))
	//: keep the parsed values for the message, in declaration order.
	parsed := make([]uint64, 0, len(values))
	//: parse.
	for _, value := range values {
		//: base 10, 64-bit, non-negative.
		number, parseErr := strconv.ParseUint(value, decimalBase, bitSize64)
		//: refuse.
		if parseErr != nil {
			//: name the entry that failed.
			return nil, rejectRule(name, tagOneOf, "the entry "+clip(value)+" is not a non-negative whole number")
		}
		//: record.
		set[number] = struct{}{}
		//: enumerate.
		parsed = append(parsed, number)
	}
	//: message built once.
	message := oneOfMessage(parsed)
	//: one map probe per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: membership.
		if _, ok := set[fieldValue.Uint()]; ok {
			//: accepted.
			return nil
		}
		//: outside the set.
		return one(path, ruleOneOf, message, CodeNotInSet)
	}, nil
}
