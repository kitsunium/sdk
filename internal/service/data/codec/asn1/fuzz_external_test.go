package asn1_test

import (
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/asn1"
)

// fuzzRecord is a typed target: an integer, two strings, a sequence of
// integers and an optional boolean. Label carries no string-type tag: Marshal
// then writes a PrintableString when the text is one and a UTF8String
// otherwise, where a printable tag would refuse the '&' encoding/asn1 reads
// in a PrintableString for the CAs that write one — a standard-library
// asymmetry, not this codec's.
type fuzzRecord struct {
	N     int64
	Name  string `asn1:"utf8"`
	Label string
	Seq   []int
	Flag  bool `asn1:"optional"`
}

// FuzzUnmarshal is #897's asn1 target: any input decodes without a panic,
// under the registered codec and NewStrict; what decodes re-encodes as DER,
// and the DER reads back to the same record — the BER relaxations
// encoding/asn1 reads are normalised, never kept.
func FuzzUnmarshal(f *testing.F) {
	seed, err := asn1.New().Marshal(fuzzRecord{N: 7, Name: "é", Label: "abc", Seq: []int{1, 2}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add(append(append([]byte{}, seed...), 0x00))
	f.Add([]byte{0x30, 0x80, 0x00, 0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, c := range []codec.Codec{asn1.New(), asn1.NewStrict()} {
			var rec fuzzRecord
			//: decoded, or refused — never a panic.
			if err := c.Unmarshal(data, &rec); err != nil {
				continue
			}
			out, err := c.Marshal(rec)
			if err != nil {
				t.Fatalf("Marshal of a decoded record %#v: %v", rec, err)
			}
			var again fuzzRecord
			//: the DER reads back to the same record.
			if err := c.Unmarshal(out, &again); err != nil || !reflect.DeepEqual(again, rec) {
				t.Fatalf("round trip of % x: %#v (%v), want %#v", out, again, err, rec)
			}
		}
	})
}
