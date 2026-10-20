package codec_test

import (
	stdjson "encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/ndjson"
)

// promotedUser is the struct an NDJSON promotion carries: not a slice, so
// the codec cannot take it natively and the facade bridges it through JSON.
type promotedUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestNDJSONAMalformedStreamIntoASliceIsRefused pins #461: a stream whose
// records do not fit the slice's element is a decoding failure, returned as
// the codec's own UNMARSHAL_FAILED — never promoted into a decode of its
// first line that drops the rest and reports success.
func TestNDJSONAMalformedStreamIntoASliceIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"first line an array":  "[1,2,3]\n\"not an int\"\n{\"x\":1}\n",
		"a bad line mid-way":   "1\n2\n\"three\"\n4\n",
		"the last line broken": "1\n2\n{",
		"a single array line":  "[1,2,3]\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got []int
			err := codec.Unmarshal(codec.NDJSON, []byte(data), &got)
			if !errs.HasCode(err, ndjson.CodeUnmarshalFailed) {
				t.Fatalf("Unmarshal(%q) = %v, %v; want UNMARSHAL_FAILED", data, got, err)
			}
			if got != nil {
				t.Fatalf("a refused stream published %v", got)
			}
		})
	}
	var ok []int
	if err := codec.Unmarshal(codec.NDJSON, []byte("1\n\n2\n3"), &ok); err != nil || !reflect.DeepEqual(ok, []int{1, 2, 3}) {
		t.Fatalf("a well-formed stream = %v, %v", ok, err)
	}
}

// TestNDJSONAPromotionContainerHoldsOneRecord pins the container's exact
// shape: a struct target is promoted from ONE record — blank lines around it
// allowed — and a stream of several records is refused PROMOTE_FAILED rather
// than decoded from its first.
func TestNDJSONAPromotionContainerHoldsOneRecord(t *testing.T) {
	t.Parallel()
	want := promotedUser{Name: "Ada", Age: 36}
	encoded, err := codec.Marshal(codec.NDJSON, want)
	if err != nil {
		t.Fatal(err)
	}
	var back promotedUser
	if err := codec.Unmarshal(codec.NDJSON, append([]byte("\n  \n"), encoded...), &back); err != nil || back != want {
		t.Fatalf("one record with blank lines = %+v, %v", back, err)
	}
	for _, data := range []string{
		`{"name":"Ada","age":36}` + "\n" + `{"name":"Eve","age":1}` + "\n",
		`{"name":"Ada","age":36}` + "\n" + `"trailing"`,
	} {
		var target promotedUser
		err := codec.Unmarshal(codec.NDJSON, []byte(data), &target)
		if !errs.HasCode(err, codec.CodePromoteFailed) {
			t.Fatalf("Unmarshal(%q) into a struct = %+v, %v; want PROMOTE_FAILED", data, target, err)
		}
	}
}

// TestNDJSONPromotionErrorsAreTyped pins rule 2 on the bridge: encoding/json's
// own failures come back as PROMOTE_FAILED, with the json error still reachable
// through errors.As, and a public text quoting nothing of the value.
func TestNDJSONPromotionErrorsAreTyped(t *testing.T) {
	t.Parallel()
	var target struct{ A int }
	err := codec.Unmarshal(codec.NDJSON, []byte(`{"A":"secret-value"}`+"\n"), &target)
	if !errs.HasCode(err, codec.CodePromoteFailed) {
		t.Fatalf("a mistyped record = %v, want PROMOTE_FAILED", err)
	}
	if _, isType := errors.AsType[*stdjson.UnmarshalTypeError](err); !isType {
		t.Fatalf("the json cause is lost: %v", err)
	}
	_, err = codec.Marshal(codec.NDJSON, map[string]any{"c": make(chan int)})
	if !errs.HasCode(err, codec.CodePromoteFailed) {
		t.Fatalf("an unencodable value = %v, want PROMOTE_FAILED", err)
	}
	if _, isType := errors.AsType[*stdjson.UnsupportedTypeError](err); !isType {
		t.Fatalf("the json cause is lost: %v", err)
	}
	for _, f := range []codec.Format{codec.CSV, codec.Form, codec.PEM} {
		if _, ferr := codec.Marshal(f, func() {}); !errs.HasCode(ferr, codec.CodePromoteFailed) {
			t.Fatalf("Marshal(%s, func) = %v, want PROMOTE_FAILED", f, ferr)
		}
	}
}
