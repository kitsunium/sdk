package jsonschema_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/app/validation"
	corejsonschema "github.com/kitsunium/sdk/internal/core/data/codec/jsonschema"
	corestrictjson "github.com/kitsunium/sdk/internal/core/data/codec/strictjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/jsonschema"
)

// orderSchema is a webhook payload's schema: nested objects, an array of
// records, the validation keywords a contract uses.
const orderSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id", "customer", "items"],
  "properties": {
    "id": {"type": "string", "pattern": "^ord_[0-9a-z]+$"},
    "customer": {"$ref": "#/$defs/customer"},
    "items": {"type": "array", "minItems": 1, "items": {"$ref": "#/$defs/item"}},
    "total": {"type": "number", "minimum": 0, "multipleOf": 0.01}
  },
  "additionalProperties": false,
  "$defs": {
    "customer": {"type": "object", "required": ["email"], "properties": {"email": {"type": "string", "format": "email"}}},
    "item": {"type": "object", "required": ["sku", "qty"], "properties": {"sku": {"type": "string", "minLength": 3}, "qty": {"type": "integer", "minimum": 1}}}
  }
}`

// mustValidator compiles schema under cfg.
func mustValidator(t *testing.T, schema string, cfg jsonschema.Config) *jsonschema.Validator {
	t.Helper()
	v, err := jsonschema.NewValidator([]byte(schema), cfg)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// TestReportFollowsTheValidationGrammar pins #882's report: every violation
// of a document, located in the validation package's path grammar, named by
// its keyword, with validation's codes where one names the keyword.
func TestReportFollowsTheValidationGrammar(t *testing.T) {
	t.Parallel()
	v := mustValidator(t, orderSchema, jsonschema.Config{AssertFormats: []string{"email"}})
	doc := `{"id":"ord_42","customer":{"email":"not an address"},"items":[{"sku":"ab","qty":0},{"qty":2}],"total":1.005,"extra":true}`
	report, err := v.Validate([]byte(doc), 1<<16)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := []string{"customer.email", "items[0].sku", "items[0].qty", "items[1].sku", "total", "extra"}
	if got := report.Paths(); !slices.Equal(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	codes := map[string]errs.Code{}
	for _, viol := range report {
		codes[viol.Path+" "+viol.Rule] = viol.Code
	}
	if codes["items[1].sku required"] != validation.CodeRequired || codes["items[0].qty minimum"] != validation.CodeOutOfRange ||
		codes["items[0].sku minLength"] != validation.CodeLengthOutOfRange || codes["total multipleOf"] != corejsonschema.CodeJSONSchemaViolation {
		t.Fatalf("codes %v", codes)
	}
	ok, err := v.Validate([]byte(`{"id":"ord_1","customer":{"email":"a@example.com"},"items":[{"sku":"abc","qty":1}],"total":12.30}`), 1<<16)
	if err != nil || !ok.OK() {
		t.Fatalf("a valid order: %v, %v", ok.Paths(), err)
	}
}

// TestViolationsNeverQuoteTheDocument plants a secret in every position that
// fails and greps the report and its error for it (the ADR 0046 shape).
func TestViolationsNeverQuoteTheDocument(t *testing.T) {
	t.Parallel()
	v := mustValidator(t, orderSchema, jsonschema.Config{AssertFormats: []string{"email"}})
	const secret = "s3cr3t-tok3n"
	doc := `{"id":"` + secret + `","customer":{"email":"` + secret + `"},"items":[{"sku":"` + secret + `","qty":"` + secret + `"}],"total":"` + secret + `","` + secret + `":1}`
	report, err := v.Validate([]byte(doc), 1<<16)
	if err != nil || report.OK() {
		t.Fatalf("Validate: %v, %v", report, err)
	}
	for _, viol := range report {
		if strings.Contains(viol.Message, secret) {
			t.Fatalf("a message quotes the document: %+v", viol)
		}
	}
	var e *errs.Error
	if rerr := report.Err(); errors.As(rerr, &e) && (strings.Contains(rerr.Error(), secret) || strings.Contains(e.Private(), secret)) {
		t.Fatalf("the report's error quotes the document: %v", rerr)
	}
}

// TestNewValidatorRefuses pins the refusals, each with its code.
func TestNewValidatorRefuses(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		schema string
		cfg    jsonschema.Config
		code   errs.Code
	}{
		"not JSON":            {`{"type":`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaSchemaInvalid},
		"not a schema":        {`42`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaSchemaInvalid},
		"wrong keyword shape": {`{"minLength":-1}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaSchemaInvalid},
		"lookahead pattern":   {`{"pattern":"(?=a)"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaSchemaInvalid},
		"draft-07":            {`{"$schema":"http://json-schema.org/draft-07/schema#"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaKeywordRefused},
		"$dynamicRef":         {`{"$dynamicRef":"#x"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaKeywordRefused},
		"unevaluated":         {`{"unevaluatedProperties":false}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaKeywordRefused},
		"content":             {`{"contentMediaType":"application/json"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaKeywordRefused},
		"remote ref":          {`{"$ref":"https://example.com/schema.json"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaRefUnresolved},
		"missing pointer":     {`{"$ref":"#/$defs/none"}`, jsonschema.Config{}, corejsonschema.CodeJSONSchemaRefUnresolved},
		"unknown format":      {`{}`, jsonschema.Config{AssertFormats: []string{"hostname"}}, corejsonschema.CodeJSONSchemaFormatUnknown},
	}
	for name, tc := range cases {
		if _, err := jsonschema.NewValidator([]byte(tc.schema), tc.cfg); !errs.HasCode(err, tc.code) {
			t.Fatalf("%s: %v, want %v", name, err, tc.code)
		}
	}
	registered := jsonschema.Config{Resources: map[string][]byte{"https://example.com/schema.json": []byte(`{"type":"string"}`)}}
	v := mustValidator(t, `{"$ref":"https://example.com/schema.json"}`, registered)
	if report, _ := v.Validate([]byte(`1`), 16); report.OK() {
		t.Fatal("a registered resource was not applied")
	}
}

// TestValidateReadsThroughStrictjson pins the document's bounds: past
// maxBytes, a duplicated name, trailing data are strictjson's refusals; a
// $ref cycle that consumes nothing is ReferenceLoop.
func TestValidateReadsThroughStrictjson(t *testing.T) {
	t.Parallel()
	v := mustValidator(t, `{"type":"object"}`, jsonschema.Config{})
	if _, err := v.Validate([]byte(`{"a":"`+strings.Repeat("x", 64)+`"}`), 16); !errs.HasCode(err, corestrictjson.CodeDocumentTooLarge) {
		t.Fatalf("past maxBytes: %v", err)
	}
	if _, err := v.Validate([]byte(`{"a":1,"a":2}`), 64); err == nil {
		t.Fatal("a duplicated name was read")
	}
	loop := mustValidator(t, `{"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}},"$ref":"#/$defs/a"}`, jsonschema.Config{})
	if _, err := loop.Validate([]byte(`1`), 16); !errs.HasCode(err, corejsonschema.CodeJSONSchemaReferenceLoop) {
		t.Fatalf("a reference loop: %v", err)
	}
}

// TestAssertFormats pins the three formats asserted on request and
// annotated otherwise.
func TestAssertFormats(t *testing.T) {
	t.Parallel()
	schema := `{"properties":{"at":{"format":"date-time"},"id":{"format":"uuid"},"to":{"format":"email"}}}`
	bad := `{"at":"yesterday","id":"123","to":"<a@b.c>"}`
	good := `{"at":"2026-11-03T12:00:00.5+01:00","id":"123e4567-e89b-12d3-a456-426614174000","to":"a@b.c"}`
	annotated := mustValidator(t, schema, jsonschema.Config{})
	if report, _ := annotated.Validate([]byte(bad), 1024); !report.OK() {
		t.Fatalf("formats asserted without being asked: %v", report.Paths())
	}
	asserted := mustValidator(t, schema, jsonschema.Config{AssertFormats: []string{"date-time", "uuid", "email"}})
	if report, _ := asserted.Validate([]byte(bad), 1024); len(report) != 3 {
		t.Fatalf("asserted formats: %v", report.Paths())
	}
	if report, _ := asserted.Validate([]byte(good), 1024); !report.OK() {
		t.Fatalf("valid formats refused: %v", report.Paths())
	}
}

// FuzzNewValidator holds the compiler to never panicking on any schema, and
// a compiled schema to never panicking on a few documents.
func FuzzNewValidator(f *testing.F) {
	f.Add([]byte(orderSchema))
	f.Add([]byte(`{"$ref":"#","allOf":[{"not":{"$ref":"#/allOf/0"}}]}`))
	f.Add([]byte(`{"$id":"urn:x","$defs":{"a":{"$anchor":"a","items":[true]}},"$ref":"urn:x#a"}`))
	f.Fuzz(func(t *testing.T, schema []byte) {
		v, err := jsonschema.NewValidator(schema, jsonschema.Config{})
		if err != nil {
			return
		}
		for _, doc := range []string{`null`, `{"a":[1,"b",{}]}`, `[[[]]]`, `"x"`, `1.5`} {
			_, _ = v.Validate([]byte(doc), 1024)
		}
	})
}
