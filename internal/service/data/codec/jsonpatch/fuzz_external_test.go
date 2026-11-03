package jsonpatch_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/jsonpatch"
)

// FuzzDiffApply is #897's jsonpatch target: for any two inputs Diff accepts,
// the patch it returns, applied in order by the RFC 6902 applier these tests
// hold, turns the first document into one Diff finds equal to the second; and
// a document against itself is no patch. Neither call panics.
func FuzzDiffApply(f *testing.F) {
	for _, seed := range [][2]string{
		{`{"a":1,"b":[1,2,3]}`, `{"a":2,"b":[1,3],"c":null}`},
		{`[1,2,3,4,5]`, `[0,1,2,3,4,5]`},
		{`{"~/":{"x":1}}`, `{"~/":{"x":1.0}}`},
		{`"s"`, `{"s":"s"}`},
		{`{"a":1,"a":2}`, `{}`},
		{`1e400`, `1`},
	} {
		f.Add([]byte(seed[0]), []byte(seed[1]))
	}
	f.Fuzz(func(t *testing.T, from, to []byte) {
		edits, err := jsonpatch.Diff(from, to)
		//: refused, or a patch — never a panic.
		if err != nil {
			return
		}
		got := mustJSON(t, applyAll(t, decoded(t, from), edits))
		rest, err := jsonpatch.Diff(got, to)
		//: the patched document is the second, as Diff compares documents.
		if err != nil || len(rest) != 0 {
			t.Fatalf("the patch %+v turns %s into %s, which differs from %s by %+v (%v)", edits, from, got, to, rest, err)
		}
		//: a document against itself.
		if same, err := jsonpatch.Diff(from, from); err != nil || len(same) != 0 {
			t.Fatalf("%s against itself = %+v, %v", from, same, err)
		}
	})
}
