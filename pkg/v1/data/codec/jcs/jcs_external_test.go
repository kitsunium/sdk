package jcs_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec/jcs"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestFacade pins canonicalization through public names: members sorted,
// whitespace dropped, numbers respelled, and a document that is not I-JSON
// refused under its code.
func TestFacade(t *testing.T) {
	t.Parallel()
	got, err := jcs.Canonicalize([]byte(`{ "b" : [1.50, -0], "a" : "x" }`))
	if err != nil || string(got) != `{"a":"x","b":[1.5,0]}` {
		t.Fatalf("Canonicalize = %s, %v", got, err)
	}
	if m, err := jcs.Marshal(map[string]int{"z": 1, "a": 2}); err != nil || string(m) != `{"a":2,"z":1}` {
		t.Fatalf("Marshal = %s, %v", m, err)
	}
	if _, err := jcs.Canonicalize([]byte(`{"a":1,"a":2}`)); !errs.HasCode(err, jcs.CodeNotJSON) {
		t.Fatalf("a duplicate name = %v, want NotJSON", err)
	}
}
