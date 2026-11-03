package jsonschema

import (
	"net/url"
	"strconv"
	"strings"

	corejsonschema "github.com/kitsunium/sdk/internal/core/data/codec/jsonschema"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// draft202012 is the one $schema this package compiles.
const draft202012 = "https://json-schema.org/draft/2020-12/schema"

// The keywords that hold schemas, by how they hold them: one, an array of
// them, an object of them. index walks only these, so an "$id" inside an
// "enum" or a "const" is data, as the specification says.
var (
	// schemaKeywords hold one schema.
	schemaKeywords = []string{"items", "contains", "additionalProperties", "propertyNames", "not", "if", "then", "else"}
	// arrayKeywords hold an array of schemas.
	arrayKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	// mapKeywords hold an object of schemas.
	mapKeywords = []string{"$defs", "properties", "patternProperties", "dependentSchemas"}
	// refusedKeywords are refused by name: the dynamic scope, the vocabulary
	// mechanism, the unevaluated and content vocabularies (out of scope, #882).
	refusedKeywords = map[string]bool{
		"$dynamicRef": true, "$dynamicAnchor": true, "$recursiveRef": true, "$recursiveAnchor": true,
		"$vocabulary": true, "unevaluatedItems": true, "unevaluatedProperties": true,
		"contentEncoding": true, "contentMediaType": true, "contentSchema": true,
	}
)

// compiler turns schema documents into checks, resolving $ref within them
// and to the resources Config registers.
type compiler struct {
	// resources are the documents and embedded resources by absolute URI,
	// fragment dropped.
	resources map[string]*value
	// bases is each schema object's base URI.
	bases map[*value]string
	// anchors are "$anchor" locations, by URI with the anchor as fragment.
	anchors map[string]*value
	// compiled memoizes each schema object, so a reference cycle ends.
	compiled map[*value]*schema
	// external are Config's resources not parsed yet, by URI.
	external map[string][]byte
	// formats are the formats asserted.
	formats map[string]bool
}

// newCompiler returns a compiler over cfg's resources and formats.
func newCompiler(cfg Config) *compiler {
	//: the asserted formats as a set.
	formats := make(map[string]bool, len(cfg.AssertFormats))
	for _, f := range cfg.AssertFormats {
		formats[f] = true
	}
	//: nothing parsed yet.
	return &compiler{
		resources: map[string]*value{}, bases: map[*value]string{}, anchors: map[string]*value{},
		compiled: map[*value]*schema{}, external: normalizeKeys(cfg.Resources), formats: formats,
	}
}

// normalizeKeys keys resources by their URI without an empty fragment.
func normalizeKeys(resources map[string][]byte) map[string][]byte {
	//: a copy, keyed as lookups key it.
	out := make(map[string][]byte, len(resources))
	for uri, doc := range resources {
		out[strings.TrimSuffix(uri, "#")] = doc
	}
	return out
}

// index records the base URI of every schema object under v, the resources
// an "$id" opens and the "$anchor" locations, walking only schema keywords.
func (c *compiler) index(v *value, base string) error {
	//: only an object carries keywords; a boolean schema has none.
	if v.kind != kindObject {
		return nil
	}
	//: an "$id" opens a resource at its URI.
	if id, ok := v.obj["$id"]; ok && id.kind == kindString {
		resolved, err := resolve(base, id.s)
		if err != nil {
			return err
		}
		base = strings.TrimSuffix(resolved, "#")
		c.resources[base] = v
	}
	c.bases[v] = base
	//: an "$anchor" names this location within its resource.
	if anchor, ok := v.obj["$anchor"]; ok && anchor.kind == kindString {
		c.anchors[base+"#"+anchor.s] = v
	}
	//: the schemas it holds.
	return c.indexChildren(v, base)
}

// indexChildren indexes the schemas held by v's schema keywords: one each,
// arrays of them, objects of them.
func (c *compiler) indexChildren(v *value, base string) error {
	//: every schema each keyword holds, in a list.
	for _, child := range heldSchemas(v) {
		if err := c.index(child, base); err != nil {
			return err
		}
	}
	return nil
}

// heldSchemas lists the schemas v's schema keywords hold.
func heldSchemas(v *value) []*value {
	//: one schema each.
	var out []*value
	for _, kw := range schemaKeywords {
		if child, ok := v.obj[kw]; ok {
			out = append(out, child)
		}
	}
	//: arrays and objects of schemas.
	return appendCollections(out, v)
}

// appendCollections appends the schemas v's array and object keywords hold.
func appendCollections(out []*value, v *value) []*value {
	//: arrays of schemas.
	for _, kw := range arrayKeywords {
		if child, ok := v.obj[kw]; ok && child.kind == kindArray {
			out = append(out, child.arr...)
		}
	}
	//: objects of schemas.
	for _, kw := range mapKeywords {
		if child, ok := v.obj[kw]; ok && child.kind == kindObject {
			out = appendMembers(out, child)
		}
	}
	return out
}

// appendMembers appends an object's member values, in order.
func appendMembers(out []*value, obj *value) []*value {
	//: by name order of the document.
	for _, name := range obj.keys {
		out = append(out, obj.obj[name])
	}
	return out
}

// addDocument parses and indexes a schema document whose retrieval URI is
// uri, and returns its root.
func (c *compiler) addDocument(uri string, doc []byte) (*value, error) {
	//: read strictly.
	root, err := parse(doc)
	if err != nil {
		return nil, schemaInvalid("a schema document is not exactly one JSON value", err)
	}
	//: the root is a resource at its retrieval URI, and at its "$id" if any.
	c.resources[uri] = root
	if err = c.index(root, uri); err != nil {
		return nil, err
	}
	return root, nil
}

// resource returns the document or embedded resource at uri: indexed
// already, or one of Config's, parsed now. Anything else is unresolved —
// a remote reference is never fetched.
func (c *compiler) resource(uri string) (*value, error) {
	//: indexed.
	if v, ok := c.resources[uri]; ok {
		return v, nil
	}
	//: registered by the caller.
	if doc, ok := c.external[uri]; ok {
		delete(c.external, uri)
		return c.addDocument(uri, doc)
	}
	//: never fetched.
	return nil, errs.Wrap(corejsonschema.RefUnresolved, errs.WrapParams{}, errs.String("ref", uri))
}

// resolveRef returns the schema value a "$ref" in the schema at base names:
// the resource it names, then the location its fragment names in it — a JSON
// Pointer, or an "$anchor".
func (c *compiler) resolveRef(base, ref string) (*value, string, error) {
	//: the reference against the base.
	target, err := resolve(base, ref)
	if err != nil {
		return nil, "", err
	}
	docURI, fragment, _ := strings.Cut(target, "#")
	//: the resource.
	root, err := c.resource(docURI)
	if err != nil {
		return nil, "", err
	}
	//: the whole resource, a pointer into it, or an anchor in it.
	switch {
	case fragment == "":
		return root, docURI, nil
	case strings.HasPrefix(fragment, "/"):
		v, perr := pointer(root, fragment)
		return v, docURI, perr
	default:
		if v, ok := c.anchors[docURI+"#"+fragment]; ok {
			return v, docURI, nil
		}
		return nil, "", errs.Wrap(corejsonschema.RefUnresolved, errs.WrapParams{}, errs.String("ref", target))
	}
}

// pointer follows the RFC 6901 JSON Pointer fragment — percent-decoded, then
// "~1" and "~0" unescaped — from root.
func pointer(root *value, fragment string) (*value, error) {
	//: the fragment's percent-encoding first.
	decoded, err := url.PathUnescape(fragment)
	if err != nil {
		return nil, schemaInvalid("a $ref fragment is not a valid pointer", err)
	}
	v := root
	for _, token := range strings.Split(decoded, "/")[1:] {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		if v = step(v, token); v == nil {
			return nil, errs.Wrap(corejsonschema.RefUnresolved, errs.WrapParams{}, errs.String("ref", fragment))
		}
	}
	return v, nil
}

// step returns the member or element token names in v, or nil.
func step(v *value, token string) *value {
	//: a member of an object.
	if v.kind == kindObject {
		return v.obj[token]
	}
	//: an element of an array, by decimal index.
	if v.kind == kindArray {
		if i, err := strconv.Atoi(token); err == nil && i >= 0 && i < len(v.arr) {
			return v.arr[i]
		}
	}
	return nil
}

// resolve resolves ref against base as RFC 3986 §5 does.
func resolve(base, ref string) (string, error) {
	//: both parsed.
	b, err := url.Parse(base)
	if err != nil {
		return "", schemaInvalid("a base URI does not parse", err)
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", schemaInvalid("a $ref or $id does not parse", err)
	}
	return b.ResolveReference(r).String(), nil
}

// compile returns the schema for v, whose base is the one index recorded —
// or fallback for a location index did not walk. A schema object is compiled
// once: a reference back to one being compiled gets the same schema, filled
// in when its compilation ends.
func (c *compiler) compile(v *value, fallback string) (*schema, error) {
	//: compiled, or being compiled.
	if s, ok := c.compiled[v]; ok {
		return s, nil
	}
	s := &schema{}
	c.compiled[v] = s
	//: a boolean schema.
	if v.kind == kindBool {
		always := v.b
		s.always = &always
		return s, nil
	}
	//: anything else but an object is not a schema.
	if v.kind != kindObject {
		return nil, schemaInvalid("a schema is neither an object nor a boolean", nil)
	}
	base, ok := c.bases[v]
	if !ok {
		base = fallback
	}
	return s, c.compileKeywords(s, v, base)
}

// compileKeywords turns each keyword of the schema object v into a check.
func (c *compiler) compileKeywords(s *schema, v *value, base string) error {
	//: the draft is checked first.
	if err := checkDraft(v); err != nil {
		return err
	}
	//: in document order, so violations come in the schema's order.
	for _, kw := range v.keys {
		//: refused by name.
		if refusedKeywords[kw] {
			return errs.Wrap(corejsonschema.KeywordRefused, errs.WrapParams{}, errs.String("keyword", kw))
		}
		build, ok := builderFor(kw)
		if !ok {
			continue
		}
		chk, err := build(keyword{c: c, parent: v, kw: v.obj[kw], base: base})
		if err != nil {
			return err
		}
		if chk != nil {
			s.checks = append(s.checks, chk)
		}
	}
	return nil
}

// checkDraft refuses a "$schema" naming anything but draft 2020-12.
func checkDraft(v *value) error {
	//: absent means 2020-12; else the 2020-12 meta-schema, with or without
	//: its empty fragment.
	draft, ok := v.obj["$schema"]
	if !ok || draft.kind == kindString && strings.TrimSuffix(draft.s, "#") == draft202012 {
		return nil
	}
	return errs.Wrap(corejsonschema.KeywordRefused, errs.WrapParams{}, errs.String("keyword", "$schema"))
}

// schemaInvalid refuses a schema, keeping cause when there is one.
func schemaInvalid(problem string, cause error) error {
	//: the sentinel, with the problem; the cause never quotes the schema.
	if cause == nil {
		return errs.Wrap(corejsonschema.SchemaInvalid, errs.WrapParams{}, errs.String("problem", problem))
	}
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corejsonschema.CodeJSONSchemaSchemaInvalid,
		Reason:  "SCHEMA_INVALID",
		Public:  "The schema is not a JSON Schema 2020-12 this validator compiles",
		Private: "service/data/codec/jsonschema: " + problem,
	})
}
