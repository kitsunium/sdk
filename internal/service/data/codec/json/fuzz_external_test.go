package json_test

import (
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// fuzzRecord is a typed target with the shapes a document usually carries.
type fuzzRecord struct {
	Name  string            `json:"name"`
	Count int64             `json:"count,omitempty"`
	Ratio float64           `json:"ratio"`
	Tags  []string          `json:"tags"`
	Attrs map[string]string `json:"attrs,omitempty"`
	Next  *fuzzRecord       `json:"next,omitempty"`
}

// FuzzUnmarshal is #897's json target: any input decodes without a panic,
// into an untyped and a typed target; what decodes re-encodes — the untyped
// value reads back equal, the typed one's second encode is the bytes of the
// first: the fixed point.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range []string{
		`{"name":"a","count":1,"ratio":0.5,"tags":["x"],"attrs":{"k":"v"},"next":{"name":"b"}}`,
		`[1,2.5,-3e10,true,false,null,"é😀"]`, `"text"`, `0`, `{}`, `{"a":{"b":[[]]}}`,
		`{"name":1}`, `{`, ``, `nul`,
	} {
		f.Add([]byte(seed))
	}
	c := json.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var untyped any
		//: decoded, or refused — never a panic.
		if err := c.Unmarshal(data, &untyped); err == nil {
			out, merr := c.Marshal(untyped)
			if merr != nil {
				t.Fatalf("Marshal of a decoded value: %v", merr)
			}
			var again any
			//: what it wrote reads back as the same value.
			if err := c.Unmarshal(out, &again); err != nil || !reflect.DeepEqual(again, untyped) {
				t.Fatalf("round trip of %q: %#v (%v), want %#v", out, again, err, untyped)
			}
		}
		var typed fuzzRecord
		//: the typed target.
		if err := c.Unmarshal(data, &typed); err == nil {
			out, merr := c.Marshal(typed)
			if merr != nil {
				t.Fatalf("Marshal of a decoded record: %v", merr)
			}
			var again fuzzRecord
			//: what it wrote reads back.
			if err := c.Unmarshal(out, &again); err != nil {
				t.Fatalf("Unmarshal of %q: %v", out, err)
			}
			//: and the second encode is the fixed point — an empty map
			//: omitempty dropped reads back nil, the bytes do not move.
			if second, err := c.Marshal(again); err != nil || string(second) != string(out) {
				t.Fatalf("second encode %q (%v), want %q", second, err, out)
			}
		}
	})
}
