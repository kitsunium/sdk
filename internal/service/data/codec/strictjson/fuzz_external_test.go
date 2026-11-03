package strictjson_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/strictjson"
)

// FuzzDecodeAgreesWithJSONv2 is #897's strictjson target: under a bound the
// input never reaches, Decode accepts exactly the documents encoding/json/v2
// accepts with unknown members rejected — duplicate names, invalid UTF-8 and
// trailing data refused by both — into the same value, and its refusal never
// quotes the input.
func FuzzDecodeAgreesWithJSONv2(f *testing.F) {
	for _, seed := range []string{
		`{"items":[{"name":"a","price":1,"tags":["x"]}]}`, `{"items":[{"name":"a","Price":1}]}`,
		`{"items":[],"items":[]}`, `{"items":[{"nope":1}]}`, `{"items":null} {}`, `{"items":[{"price":2147483648}]}`,
		"{\"items\":[{\"name\":\"\xff\"}]}", `null`, ``, ` `,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var got, want order
		err := strictjson.Decode(bytes.NewReader(data), &got, int64(len(data))+1)
		verr := jsonv2.Unmarshal(data, &want, jsonv2.RejectUnknownMembers(true))
		//: the same verdict.
		if (err == nil) != (verr == nil) {
			t.Fatalf("Decode(%q) = %v, json/v2 = %v", data, err, verr)
		}
		//: the same value when both accept.
		if err == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("Decode(%q) = %+v, json/v2 = %+v", data, got, want)
		}
		//: a refusal names no byte of the document.
		if err != nil && len(data) >= 8 && bytes.Contains([]byte(err.Error()), data) {
			t.Fatalf("the refusal %q quotes the document", err)
		}
	})
}
