package jsonpatch_test

import (
	"encoding/json"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/codec/jsonpatch"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestFacade pins the diff through public names: nested changes as RFC 6902
// operations with both values, a JSON Patch document when encoded, and the
// refusal of a document that is not JSON under its code.
func TestFacade(t *testing.T) {
	t.Parallel()
	edits, err := jsonpatch.Diff(
		[]byte(`{"title":"Draft","blocks":[{"text":"a"},{"text":"b"}]}`),
		[]byte(`{"title":"Final","blocks":[{"text":"a"},{"text":"b"},{"text":"c"}]}`),
	)
	if err != nil {
		t.Fatalf("Diff() = %v", err)
	}
	raw, err := json.Marshal(edits)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	want := `[{"op":"add","path":"/blocks/2","value":{"text":"c"}},{"op":"replace","path":"/title","value":"Final","old":"Draft"}]`
	if string(raw) != want {
		t.Fatalf("the patch is\n%s\nwant\n%s", raw, want)
	}
	if edits[1].Op != jsonpatch.Replace || edits[0].Op != jsonpatch.Add {
		t.Fatalf("the operations are %v and %v", edits[0].Op, edits[1].Op)
	}
	if _, err := jsonpatch.Diff([]byte(`{`), []byte(`{}`)); !errs.HasCode(err, jsonpatch.CodeNotJSON) {
		t.Fatalf("a document that is not JSON = %v, want NotJSON", err)
	}
}
