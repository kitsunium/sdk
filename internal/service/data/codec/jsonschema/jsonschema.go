package jsonschema

import (
	"bytes"
	"encoding/json/jsontext"

	"github.com/kitsunium/sdk/internal/core/app/validation"
	corejsonschema "github.com/kitsunium/sdk/internal/core/data/codec/jsonschema"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/strictjson"
)

// Config configures NewValidator. The zero Config compiles a self-contained
// schema with every format an annotation.
type Config struct {
	// Resources are schema documents a "$ref" may name, by URI: each is a
	// resource at that URI, and at its "$id" when it has one. A reference to
	// any other URI is refused; nothing is fetched.
	Resources map[string][]byte
	// AssertFormats are the formats asserted rather than annotated:
	// "date-time" (RFC 3339), "uuid" and "email" (an addr-spec). Naming
	// another is refused.
	AssertFormats []string
}

// Validator is a compiled JSON Schema 2020-12: immutable, safe for
// concurrent use, reused for every document it validates.
type Validator struct {
	// root is the compiled root schema.
	root *schema
}

// NewValidator compiles schema, a JSON Schema 2020-12 document, under cfg.
// It refuses with SchemaInvalid a schema that is not exactly one JSON value
// or whose keyword has the wrong shape, KeywordRefused one using a keyword
// out of scope — "$dynamicRef" and "$dynamicAnchor", "$vocabulary",
// "unevaluatedItems" and "unevaluatedProperties", the content vocabulary — or
// another draft's "$schema", RefUnresolved a "$ref" naming neither the
// document nor a registered resource, and FormatUnknown a format cfg
// asserts that this package cannot check.
func NewValidator(schema []byte, cfg Config) (*Validator, error) {
	//: the formats first: refused before the schema is read.
	if err := checkFormats(cfg.AssertFormats); err != nil {
		return nil, err
	}
	c := newCompiler(cfg)
	root, err := c.addDocument("", schema)
	if err != nil {
		return nil, err
	}
	s, err := c.compile(root, "")
	if err != nil {
		return nil, err
	}
	return &Validator{root: s}, nil
}

// Validate reads doc through strictjson's bounds — at most maxBytes, one
// value, no duplicated member name — and returns the violations of the
// schema, located in the validation package's path grammar
// ("items[2].name"). A message never quotes the document. An empty report
// means doc is valid; an error means doc was not read: strictjson's
// refusal, or ReferenceLoop for a "$ref" cycle that consumes nothing.
func (v *Validator) Validate(doc []byte, maxBytes int64) (validation.ReportValue, error) {
	//: read strictly and bounded, into the raw value.
	var raw jsontext.Value
	if err := strictjson.Decode(bytes.NewReader(doc), &raw, maxBytes); err != nil {
		return nil, err
	}
	instance, err := parse(raw)
	if err != nil {
		return nil, errs.Wrap(err, errs.WrapParams{
			Code:    corejsonschema.CodeJSONSchemaSchemaInvalid,
			Reason:  "SCHEMA_INVALID",
			Public:  "The document could not be read",
			Private: "service/data/codec/jsonschema.Validate: a document strictjson accepted did not parse",
		})
	}
	e := &eval{collect: true}
	v.root.validate(e, instance, validation.RootPath)
	if e.looped {
		return nil, corejsonschema.ReferenceLoop
	}
	return e.report, nil
}
