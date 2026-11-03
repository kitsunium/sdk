package jsonschema_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec/jsonschema"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestFacade pins validation through public names: a report located in the
// validation grammar, and a refused keyword under its code.
func TestFacade(t *testing.T) {
	t.Parallel()
	v, err := jsonschema.NewValidator([]byte(`{"type":"object","required":["name"],"properties":{"tags":{"items":{"type":"string"}}}}`), jsonschema.Config{})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	report, err := v.Validate([]byte(`{"tags":["a",2]}`), 1024)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if paths := report.Paths(); len(paths) != 2 || paths[0] != "name" || paths[1] != "tags[1]" {
		t.Fatalf("paths %v", paths)
	}
	if _, err := jsonschema.NewValidator([]byte(`{"unevaluatedProperties":false}`), jsonschema.Config{}); !errs.HasCode(err, jsonschema.CodeKeywordRefused) {
		t.Fatalf("a refused keyword: %v", err)
	}
}
