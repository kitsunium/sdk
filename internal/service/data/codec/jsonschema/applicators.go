package jsonschema

import (
	"regexp"
	"strconv"

	"github.com/kitsunium/sdk/internal/core/app/validation"
)

// patternSchema is one "patternProperties" entry.
type patternSchema struct {
	// re matches the member names the schema applies to.
	re *regexp.Regexp
	// s is the schema.
	s *schema
}

// buildRef compiles "$ref": the schema it names, resolved now — within the
// document, or in a resource Config registers; never fetched.
func buildRef(k keyword) (check, error) {
	//: a URI reference.
	if k.kw.kind != kindString {
		return nil, keywordInvalid("$ref")
	}
	target, targetBase, err := k.c.resolveRef(k.base, k.kw.s)
	if err != nil {
		return nil, err
	}
	s, err := k.c.compile(target, targetBase)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		return s.validate(e, v, path)
	}, nil
}

// compileList compiles an array of schemas.
func compileList(c *compiler, kw *value, name, base string) ([]*schema, error) {
	//: a non-empty array.
	if kw.kind != kindArray {
		return nil, keywordInvalid(name)
	}
	out := make([]*schema, 0, len(kw.arr))
	for _, item := range kw.arr {
		s, err := c.compile(item, base)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// buildCombinator compiles "allOf", "anyOf" or "oneOf". allOf's violations
// are its schemas' own; anyOf and oneOf ask their schemas quietly and report
// once.
func buildCombinator(name string) builder {
	return func(k keyword) (check, error) {
		//: the schemas.
		list, err := compileList(k.c, k.kw, name, k.base)
		if err != nil {
			return nil, err
		}
		if name == "allOf" {
			return allOf(list), nil
		}
		return countMatches(name, list), nil
	}
}

// allOf passes when every schema does; each reports its own violations.
func allOf(list []*schema) check {
	return func(e *eval, v *value, path string) bool {
		ok := true
		for _, s := range list {
			if !s.validate(e, v, path) {
				ok = false
				if !e.collect {
					return false
				}
			}
		}
		return ok
	}
}

// countMatches passes anyOf when at least one schema matches, oneOf when
// exactly one does.
func countMatches(name string, list []*schema) check {
	message := "must match at least one of the schemas"
	if name == "oneOf" {
		message = "must match exactly one of the schemas"
	}
	return func(e *eval, v *value, path string) bool {
		matches := 0
		for _, s := range list {
			q := e.quiet()
			if s.validate(q, v, path) {
				matches++
			}
			e.absorb(q)
			if name == "anyOf" && matches > 0 {
				return true
			}
		}
		if matches == 1 && name == "oneOf" {
			return true
		}
		return e.fail(path, name, message, violationCode)
	}
}

// buildNot compiles "not".
func buildNot(k keyword) (check, error) {
	//: one schema, asked quietly.
	s, err := k.c.compile(k.kw, k.base)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		q := e.quiet()
		matched := s.validate(q, v, path)
		e.absorb(q)
		if !matched {
			return true
		}
		return e.fail(path, "not", "must not match the schema", violationCode)
	}, nil
}

// buildIf compiles "if" with its siblings "then" and "else": the branch the
// condition picks applies, and reports its own violations.
func buildIf(k keyword) (check, error) {
	//: the condition, and each branch when present.
	cond, err := k.c.compile(k.kw, k.base)
	if err != nil {
		return nil, err
	}
	then, err := optionalSchema(k.c, k.parent, "then", k.base)
	if err != nil {
		return nil, err
	}
	otherwise, err := optionalSchema(k.c, k.parent, "else", k.base)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		q := e.quiet()
		branch := otherwise
		if cond.validate(q, v, path) {
			branch = then
		}
		e.absorb(q)
		return branch == nil || branch.validate(e, v, path)
	}, nil
}

// optionalSchema compiles the sibling keyword name when parent has it.
func optionalSchema(c *compiler, parent *value, name, base string) (*schema, error) {
	//: absent is nil.
	kw, ok := parent.obj[name]
	if !ok {
		return nil, nil
	}
	return c.compile(kw, base)
}

// buildDependentSchemas compiles "dependentSchemas": for each member present,
// its schema applies to the whole object.
func buildDependentSchemas(k keyword) (check, error) {
	//: an object of schemas.
	if k.kw.kind != kindObject {
		return nil, keywordInvalid("dependentSchemas")
	}
	deps := make(map[string]*schema, len(k.kw.keys))
	for _, name := range k.kw.keys {
		s, err := k.c.compile(k.kw.obj[name], k.base)
		if err != nil {
			return nil, err
		}
		deps[name] = s
	}
	keys := k.kw.keys
	return func(e *eval, v *value, path string) bool {
		ok := true
		for _, name := range keys {
			if _, present := v.obj[name]; present && v.kind == kindObject {
				ok = deps[name].validate(e, v, path) && ok
			}
		}
		return ok
	}, nil
}

// buildItems compiles "prefixItems" — a schema per position — or "items" —
// the schema of every element after prefixItems' positions.
func buildItems(name string) builder {
	return func(k keyword) (check, error) {
		//: prefixItems: a list, the first positions.
		if name == "prefixItems" {
			list, err := compileList(k.c, k.kw, name, k.base)
			if err != nil {
				return nil, err
			}
			return elements(list, nil, 0), nil
		}
		//: items: one schema, after prefixItems' positions.
		s, err := k.c.compile(k.kw, k.base)
		if err != nil {
			return nil, err
		}
		skip := 0
		if prefix, ok := k.parent.obj["prefixItems"]; ok && prefix.kind == kindArray {
			skip = len(prefix.arr)
		}
		return elements(nil, s, skip), nil
	}
}

// elements checks an array: list's schemas at their positions, rest at every
// position from skip on.
func elements(list []*schema, rest *schema, skip int) check {
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindArray {
			return true
		}
		ok := true
		for i, elem := range v.arr {
			s := rest
			if i < len(list) {
				s = list[i]
			} else if rest == nil || i < skip {
				continue
			}
			if !s.validate(e, elem, validation.JoinIndex(path, i)) {
				ok = false
				if !e.collect {
					return false
				}
			}
		}
		return ok
	}
}

// buildContains compiles "contains" with its siblings "minContains" (one by
// default) and "maxContains".
func buildContains(k keyword) (check, error) {
	//: the schema, and the bounds on how many elements match it.
	s, err := k.c.compile(k.kw, k.base)
	if err != nil {
		return nil, err
	}
	lo, hi, err := containsBounds(k.parent)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindArray {
			return true
		}
		n := 0
		for i, elem := range v.arr {
			q := e.quiet()
			if s.validate(q, elem, validation.JoinIndex(path, i)) {
				n++
			}
			e.absorb(q)
		}
		if n >= lo && (hi < 0 || n <= hi) {
			return true
		}
		return e.fail(path, "contains", "must hold between "+strconv.Itoa(lo)+" and the allowed number of matching elements", violationCode)
	}, nil
}

// containsBounds reads minContains (1 when absent) and maxContains (-1, no
// bound, when absent).
func containsBounds(parent *value) (lo, hi int, err error) {
	//: the defaults, then each bound present.
	lo, hi = 1, -1
	if kw, ok := parent.obj["minContains"]; ok {
		if lo, err = nonNegative(kw, "minContains"); err != nil {
			return 0, 0, err
		}
	}
	if kw, ok := parent.obj["maxContains"]; ok {
		if hi, err = nonNegative(kw, "maxContains"); err != nil {
			return 0, 0, err
		}
	}
	return lo, hi, nil
}

// buildMembers compiles "properties", "patternProperties" or
// "additionalProperties" — the last applying to every member neither of the
// others names.
func buildMembers(name string) builder {
	return func(k keyword) (check, error) {
		//: by keyword.
		switch name {
		case "properties":
			return buildProperties(k)
		case "patternProperties":
			return buildPatternProperties(k)
		default:
			return buildAdditional(k)
		}
	}
}

// compileMap compiles an object of schemas.
func compileMap(c *compiler, kw *value, name, base string) (map[string]*schema, error) {
	//: an object.
	if kw.kind != kindObject {
		return nil, keywordInvalid(name)
	}
	out := make(map[string]*schema, len(kw.keys))
	for _, member := range kw.keys {
		s, err := c.compile(kw.obj[member], base)
		if err != nil {
			return nil, err
		}
		out[member] = s
	}
	return out, nil
}

// buildProperties compiles "properties".
func buildProperties(k keyword) (check, error) {
	//: the schema of each named member.
	props, err := compileMap(k.c, k.kw, "properties", k.base)
	if err != nil {
		return nil, err
	}
	keys := k.kw.keys
	return func(e *eval, v *value, path string) bool {
		ok := true
		for _, name := range keys {
			if member, present := v.obj[name]; present && v.kind == kindObject {
				ok = props[name].validate(e, member, validation.JoinField(path, name)) && ok
			}
		}
		return ok
	}, nil
}

// compilePatterns compiles "patternProperties".
func compilePatterns(c *compiler, kw *value, base string) ([]patternSchema, error) {
	//: each pattern and its schema.
	schemas, err := compileMap(c, kw, "patternProperties", base)
	if err != nil {
		return nil, err
	}
	out := make([]patternSchema, 0, len(kw.keys))
	for _, pattern := range kw.keys {
		re, rerr := regexp.Compile(pattern)
		if rerr != nil {
			return nil, schemaInvalid("a patternProperties pattern is not a regular expression Go's regexp reads", rerr)
		}
		out = append(out, patternSchema{re: re, s: schemas[pattern]})
	}
	return out, nil
}

// buildPatternProperties compiles "patternProperties".
func buildPatternProperties(k keyword) (check, error) {
	//: the schema of each member a pattern matches.
	patterns, err := compilePatterns(k.c, k.kw, k.base)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindObject {
			return true
		}
		ok := true
		for _, name := range v.keys {
			for _, p := range patterns {
				if p.re.MatchString(name) {
					ok = p.s.validate(e, v.obj[name], validation.JoinField(path, name)) && ok
				}
			}
		}
		return ok
	}, nil
}

// buildAdditional compiles "additionalProperties" against its siblings.
func buildAdditional(k keyword) (check, error) {
	//: the schema, and what the siblings name.
	s, err := k.c.compile(k.kw, k.base)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	if props, ok := k.parent.obj["properties"]; ok && props.kind == kindObject {
		for _, name := range props.keys {
			named[name] = true
		}
	}
	var patterns []patternSchema
	if pp, ok := k.parent.obj["patternProperties"]; ok {
		if patterns, err = compilePatterns(k.c, pp, k.base); err != nil {
			return nil, err
		}
	}
	return additional(s, named, patterns), nil
}

// additional applies s to every member neither named nor matched.
func additional(s *schema, named map[string]bool, patterns []patternSchema) check {
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindObject {
			return true
		}
		ok := true
		for _, name := range v.keys {
			if named[name] || matchesAny(patterns, name) {
				continue
			}
			ok = s.validate(e, v.obj[name], validation.JoinField(path, name)) && ok
		}
		return ok
	}
}

// matchesAny reports whether a pattern matches name.
func matchesAny(patterns []patternSchema, name string) bool {
	//: any.
	for _, p := range patterns {
		if p.re.MatchString(name) {
			return true
		}
	}
	return false
}

// buildPropertyNames compiles "propertyNames": each member name, as a
// string, against the schema; a failure is reported at the member.
func buildPropertyNames(k keyword) (check, error) {
	//: the schema.
	s, err := k.c.compile(k.kw, k.base)
	if err != nil {
		return nil, err
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindObject {
			return true
		}
		ok := true
		for _, name := range v.keys {
			q := e.quiet()
			matched := s.validate(q, &value{kind: kindString, s: name}, path)
			e.absorb(q)
			if !matched {
				ok = e.fail(validation.JoinField(path, name), "propertyNames", "the member name does not match the schema", violationCode)
			}
		}
		return ok
	}, nil
}
