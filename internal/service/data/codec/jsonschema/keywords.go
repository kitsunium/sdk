package jsonschema

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/app/validation"
	corejsonschema "github.com/kitsunium/sdk/internal/core/data/codec/jsonschema"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Keyword constants.
const (
	// violationCode is the code of a violation no validation code names:
	// type, multipleOf, the combinators, the array and object applicators,
	// format.
	violationCode errs.Code = corejsonschema.CodeJSONSchemaViolation
	// maxBoundDecimals caps the decimals a bound prints with.
	maxBoundDecimals int = 6
)

// keyword is one keyword being compiled: the compiler, the schema object
// that holds it, its value, and the base URI of that schema object.
type keyword struct {
	// c is the compiler, for the schemas the keyword holds.
	c *compiler
	// parent is the schema object, for the keyword's siblings.
	parent *value
	// kw is the keyword's value.
	kw *value
	// base is the parent's base URI.
	base string
}

// builder compiles one keyword into a check — or nil for a keyword that
// asserts nothing on its own.
type builder func(k keyword) (check, error)

// builderFor returns the builder of a keyword, or false for an annotation or
// an unknown keyword, which assert nothing.
func builderFor(kw string) (builder, bool) {
	//: the validation vocabulary first, then the applicators.
	if b, ok := validationBuilder(kw); ok {
		return b, true
	}
	return applicatorBuilder(kw)
}

// validationBuilder returns the builders of the validation vocabulary.
func validationBuilder(kw string) (builder, bool) {
	//: by keyword.
	switch kw {
	case "type":
		return buildType, true
	case "enum":
		return buildEnum, true
	case "const":
		return buildConst, true
	case "multipleOf":
		return buildMultipleOf, true
	case "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum":
		return buildBound(kw), true
	case "maxLength", "minLength", "maxItems", "minItems", "maxProperties", "minProperties":
		return buildSize(kw), true
	default:
		return validationBuilder2(kw)
	}
}

// validationBuilder2 returns the rest of the validation vocabulary's builders.
func validationBuilder2(kw string) (builder, bool) {
	//: by keyword.
	switch kw {
	case "pattern":
		return buildPattern, true
	case "uniqueItems":
		return buildUniqueItems, true
	case "required":
		return buildRequired, true
	case "dependentRequired":
		return buildDependentRequired, true
	case "format":
		return buildFormat, true
	default:
		return nil, false
	}
}

// applicatorBuilder returns the builders of the core and applicator
// vocabularies.
func applicatorBuilder(kw string) (builder, bool) {
	//: by keyword.
	switch kw {
	case "$ref":
		return buildRef, true
	case "allOf", "anyOf", "oneOf":
		return buildCombinator(kw), true
	case "not":
		return buildNot, true
	case "if":
		return buildIf, true
	case "dependentSchemas":
		return buildDependentSchemas, true
	default:
		return applicatorBuilder2(kw)
	}
}

// applicatorBuilder2 returns the array and object applicators' builders.
func applicatorBuilder2(kw string) (builder, bool) {
	//: by keyword.
	switch kw {
	case "prefixItems", "items":
		return buildItems(kw), true
	case "contains":
		return buildContains, true
	case "properties", "patternProperties", "additionalProperties":
		return buildMembers(kw), true
	case "propertyNames":
		return buildPropertyNames, true
	default:
		return nil, false
	}
}

// keywordInvalid refuses a keyword whose value has the wrong shape.
func keywordInvalid(kw string) error {
	//: named, never quoted.
	return errs.Wrap(corejsonschema.SchemaInvalid, errs.WrapParams{}, errs.String("keyword", kw))
}

// typeMatches reports whether v is of the JSON Schema type name.
func typeMatches(v *value, name string) bool {
	//: by name; "integer" is a number with no fractional part.
	switch name {
	case "null":
		return v.kind == kindNull
	case "boolean":
		return v.kind == kindBool
	case "object":
		return v.kind == kindObject
	case "array":
		return v.kind == kindArray
	case "number":
		return v.kind == kindNumber
	case "integer":
		return v.isInteger()
	case "string":
		return v.kind == kindString
	default:
		return false
	}
}

// buildType compiles "type": a name or an array of names.
func buildType(k keyword) (check, error) {
	//: one name, or several.
	names, err := stringList(k.kw, "type")
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		for _, name := range names {
			if typeMatches(v, name) {
				return true
			}
		}
		return e.fail(path, "type", "must be of the type the schema names", violationCode)
	}, nil
}

// stringList reads a string or an array of strings.
func stringList(kw *value, name string) ([]string, error) {
	//: a single string.
	if kw.kind == kindString {
		return []string{kw.s}, nil
	}
	if kw.kind != kindArray {
		return nil, keywordInvalid(name)
	}
	out := make([]string, 0, len(kw.arr))
	for _, item := range kw.arr {
		if item.kind != kindString {
			return nil, keywordInvalid(name)
		}
		out = append(out, item.s)
	}
	return out, nil
}

// buildEnum compiles "enum".
func buildEnum(k keyword) (check, error) {
	//: an array of allowed values.
	if k.kw.kind != kindArray {
		return nil, keywordInvalid("enum")
	}
	return func(e *eval, v *value, path string) bool {
		for _, allowed := range k.kw.arr {
			if equal(v, allowed) {
				return true
			}
		}
		return e.fail(path, "enum", "must be one of the allowed values", validation.CodeNotInSet)
	}, nil
}

// buildConst compiles "const".
func buildConst(k keyword) (check, error) {
	//: the one allowed value.
	return func(e *eval, v *value, path string) bool {
		if equal(v, k.kw) {
			return true
		}
		return e.fail(path, "const", "must be the value the schema requires", validation.CodeNotInSet)
	}, nil
}

// buildMultipleOf compiles "multipleOf": a number above zero.
func buildMultipleOf(k keyword) (check, error) {
	//: a strictly positive number.
	if k.kw.kind != kindNumber || k.kw.n.Sign() <= 0 {
		return nil, keywordInvalid("multipleOf")
	}
	message := "must be a multiple of " + k.kw.n.RatString()
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindNumber || new(big.Rat).Quo(v.n, k.kw.n).IsInt() {
			return true
		}
		return e.fail(path, "multipleOf", message, violationCode)
	}, nil
}

// buildBound compiles one of the four numeric bounds.
func buildBound(name string) builder {
	return func(k keyword) (check, error) {
		//: a number.
		if k.kw.kind != kindNumber {
			return nil, keywordInvalid(name)
		}
		bound := k.kw.n
		//: an integer prints no decimal.
		precision := 0
		if !bound.IsInt() {
			precision = maxBoundDecimals
		}
		message := boundMessage(name, bound.FloatString(precision))
		return func(e *eval, v *value, path string) bool {
			if v.kind != kindNumber || withinBound(name, v.n.Cmp(bound)) {
				return true
			}
			return e.fail(path, name, message, validation.CodeOutOfRange)
		}, nil
	}
}

// withinBound reports whether cmp, the instance compared with the bound,
// satisfies the named bound.
func withinBound(name string, cmp int) bool {
	//: by bound.
	switch name {
	case "maximum":
		return cmp <= 0
	case "exclusiveMaximum":
		return cmp < 0
	case "minimum":
		return cmp >= 0
	default:
		return cmp > 0
	}
}

// boundMessage words a numeric bound, b being its text; the bound is the
// schema's, never the instance's.
func boundMessage(name, b string) string {
	switch name {
	case "maximum":
		return "must be at most " + b
	case "exclusiveMaximum":
		return "must be less than " + b
	case "minimum":
		return "must be at least " + b
	default:
		return "must be greater than " + b
	}
}

// sizeOf measures v for a size keyword: characters, elements or members;
// -1 when the keyword does not apply to v's kind.
func sizeOf(name string, v *value) int {
	//: by keyword family.
	switch name {
	case "maxLength", "minLength":
		if v.kind == kindString {
			return utf8.RuneCountInString(v.s)
		}
	case "maxItems", "minItems":
		if v.kind == kindArray {
			return len(v.arr)
		}
	default:
		if v.kind == kindObject {
			return len(v.keys)
		}
	}
	return -1
}

// nonNegative reads a size keyword: a non-negative integer, 2.0 included.
func nonNegative(kw *value, name string) (int, error) {
	//: a number with no fraction, not below zero, that fits an int.
	if !kw.isInteger() || kw.n.Sign() < 0 || !kw.n.Num().IsInt64() {
		return 0, keywordInvalid(name)
	}
	//: past what an int holds (a 32-bit platform), no size reaches it anyway.
	return int(min(kw.n.Num().Int64(), int64(math.MaxInt))), nil
}

// buildSize compiles one of the six size bounds.
func buildSize(name string) builder {
	return func(k keyword) (check, error) {
		//: a non-negative integer.
		limit, err := nonNegative(k.kw, name)
		if err != nil {
			return nil, err
		}
		isMax := name[:3] == "max"
		message := "must have at least " + strconv.Itoa(limit)
		if isMax {
			message = "must have at most " + strconv.Itoa(limit)
		}
		return func(e *eval, v *value, path string) bool {
			size := sizeOf(name, v)
			if size < 0 || (isMax && size <= limit) || (!isMax && size >= limit) {
				return true
			}
			return e.fail(path, name, message, validation.CodeLengthOutOfRange)
		}, nil
	}
}

// buildPattern compiles "pattern": an ECMA-262 regular expression, read by
// Go's regexp — a pattern RE2 does not accept (a lookaround, a
// backreference) is refused when the schema is compiled.
func buildPattern(k keyword) (check, error) {
	//: a string that compiles.
	if k.kw.kind != kindString {
		return nil, keywordInvalid("pattern")
	}
	re, err := regexp.Compile(k.kw.s)
	if err != nil {
		return nil, schemaInvalid("a pattern is not a regular expression Go's regexp reads", err)
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindString || re.MatchString(v.s) {
			return true
		}
		return e.fail(path, "pattern", "must match the pattern the schema gives", validation.CodePatternMismatch)
	}, nil
}

// buildUniqueItems compiles "uniqueItems".
func buildUniqueItems(k keyword) (check, error) {
	//: a boolean; false asserts nothing.
	if k.kw.kind != kindBool {
		return nil, keywordInvalid("uniqueItems")
	}
	if !k.kw.b {
		return nil, nil
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindArray || unique(v.arr) {
			return true
		}
		return e.fail(path, "uniqueItems", "must not hold the same element twice", violationCode)
	}, nil
}

// unique reports whether no two elements are equal.
func unique(arr []*value) bool {
	//: every pair.
	for i := range arr {
		for j := i + 1; j < len(arr); j++ {
			if equal(arr[i], arr[j]) {
				return false
			}
		}
	}
	return true
}

// buildRequired compiles "required".
func buildRequired(k keyword) (check, error) {
	//: an array of names.
	names, err := stringList(k.kw, "required")
	if err != nil || k.kw.kind != kindArray {
		return nil, keywordInvalid("required")
	}
	return func(e *eval, v *value, path string) bool {
		return requireMembers(e, v, path, names, "required")
	}, nil
}

// requireMembers fails once per name v lacks, at the member's path.
func requireMembers(e *eval, v *value, path string, names []string, rule string) bool {
	//: only an object has members.
	if v.kind != kindObject {
		return true
	}
	ok := true
	for _, name := range names {
		if _, present := v.obj[name]; !present {
			ok = e.fail(validation.JoinField(path, name), rule, "is required", validation.CodeRequired)
			if !e.collect {
				return false
			}
		}
	}
	return ok
}

// buildDependentRequired compiles "dependentRequired".
func buildDependentRequired(k keyword) (check, error) {
	//: an object of name arrays.
	if k.kw.kind != kindObject {
		return nil, keywordInvalid("dependentRequired")
	}
	deps := make(map[string][]string, len(k.kw.keys))
	for _, name := range k.kw.keys {
		list, err := stringList(k.kw.obj[name], "dependentRequired")
		if err != nil {
			return nil, err
		}
		deps[name] = list
	}
	keys := k.kw.keys
	return func(e *eval, v *value, path string) bool {
		ok := true
		for _, name := range keys {
			if _, present := v.obj[name]; present && v.kind == kindObject {
				ok = requireMembers(e, v, path, deps[name], "dependentRequired") && ok
			}
		}
		return ok
	}, nil
}
