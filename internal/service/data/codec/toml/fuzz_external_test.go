// Package toml_test — fuzz targets. The decoder takes bytes nobody vetted, so
// it must never panic, never recurse past its cap, and whatever it accepts the
// encoder must write back to a document that decodes to the same value.
package toml_test

import (
	"bytes"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/toml"
)

// fuzzSeeds are documents covering every production of the grammar, valid and
// not, as the mutator's starting points.
var fuzzSeeds = []string{
	"",
	"# comment only\n",
	"title = \"TOML Example\"\n\n[owner]\nname = \"Tom Preston-Werner\"\ndob = 1979-05-27T07:32:00-08:00\n",
	"[database]\nenabled = true\nports = [ 8000, 8001, 8002 ]\ndata = [ [\"delta\", \"phi\"], [3.14] ]\ntemp_targets = { cpu = 79.5, case = 72.0 }\n",
	"[servers]\n\n[servers.alpha]\nip = \"10.0.0.1\"\nrole = \"frontend\"\n",
	"[[products]]\nname = \"Hammer\"\nsku = 738594937\n\n[[products]]\n\n[[products]]\nname = \"Nail\"\n",
	"[[fruits]]\nname = \"apple\"\n[fruits.physical]\ncolor = \"red\"\n[[fruits.varieties]]\nname = \"red delicious\"\n",
	"a.b.c = 1\na.d = 2\n\"quoted.key\" = 3\n'literal' = 4\n",
	"str = \"\\u00E9\\U0001F600 \\\" \\\\ \\b\\f\\n\\r\\t\"\nlit = 'C:\\path'\n",
	"ml = \"\"\"\nRoses are red\\\n   Violets are blue\"\"\"\nmll = '''\nraw\\n'''\n",
	"q = \"\"\"Here are two quotation marks: \"\". Simple enough.\"\"\"\nr = '''''That,' she said'''''\n",
	"i = +99\nj = -17\nk = 1_000\nh = 0xDEAD_beef\no = 0o755\nb = 0b1101\n",
	"f = +1.0\ng = 3.1415\nh = -0.01\ni = 5e+22\nj = 6.626e-34\nk = 224_617.445_991_228\nl = inf\nm = -nan\n",
	"odt = 1979-05-27T00:32:00.999999-07:00\nldt = 1979-05-27 07:32:00\nld = 1979-05-27\nlt = 00:32:00.999999999999\n",
	"inline = { x = 1, y = { z = [1, {w = 2}] } }\n",
	"t = {\n  a = 1, # v1.1.0\n  b = 2,\n}\nx = \"\\x41\\e\"\nlt = 07:32\n",
	"a = 1\na = 2\n",
	"[t]\n[t]\n",
	"a = [1,,2]\n",
	"a = 01\n",
	"a = \"\\uD800\"\n",
	"a = [[[[[[[[[[[[[[[[[[[[1]]]]]]]]]]]]]]]]]]]]\n",
	"a = '\xff'\n",
	"a\r\nb = 1\n",
}

// FuzzUnmarshal decodes arbitrary bytes into map[string]any. It must not
// panic; a refusal must be UNMARSHAL_FAILED; and an accepted document must
// encode, decode back to the same value, and encode again to the same bytes.
func FuzzUnmarshal(f *testing.F) {
	//: the corpus.
	for _, seed := range fuzzSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		var first map[string]any
		err := toml.New().Unmarshal(doc, &first)
		//: a refusal is always the codec's own.
		if err != nil {
			if !errs.HasReason(err, "UNMARSHAL_FAILED") {
				t.Fatalf("refusal is not UNMARSHAL_FAILED: %v", err)
			}
			return
		}
		encoded, err := toml.New().Marshal(first)
		//: whatever the decoder accepts, the encoder writes.
		if err != nil {
			t.Fatalf("Marshal of a decoded document: %v %v\ndoc %q", err, errs.FieldsOf(err), doc)
		}
		var second map[string]any
		//: and what it writes decodes.
		if err := toml.New().Unmarshal(encoded, &second); err != nil {
			t.Fatalf("re-decode: %v %v\nencoded %q", err, errs.FieldsOf(err), encoded)
		}
		//: to the same value.
		if !sameValue(first, second) {
			t.Fatalf("round trip changed the value\ndoc     %q\nencoded %q", doc, encoded)
		}
		again, err := toml.New().Marshal(second)
		//: and encodes to the same bytes.
		if err != nil || !bytes.Equal(encoded, again) {
			t.Fatalf("second encode differs: %v\n%q\n%q", err, encoded, again)
		}
	})
}

// fuzzTarget is a typed target with a field of every kind the decoder
// converts into.
type fuzzTarget struct {
	S   string             `toml:"s"`
	I   int8               `toml:"i"`
	U   uint               `toml:"u"`
	F   float32            `toml:"f"`
	B   bool               `toml:"b"`
	T   time.Time          `toml:"t"`
	LD  toml.LocalDate     `toml:"ld"`
	Arr [2]int             `toml:"arr"`
	Sl  []string           `toml:"sl"`
	M   map[string]float64 `toml:"m"`
	MK  map[int]bool       `toml:"mk"`
	P   *fuzzTarget        `toml:"p"`
	Any any                `toml:"any"`
	AOT []fuzzTarget       `toml:"aot"`
}

// FuzzUnmarshalTyped decodes arbitrary bytes into a typed target: it must not
// panic, and a refusal must be UNMARSHAL_FAILED.
func FuzzUnmarshalTyped(f *testing.F) {
	//: the shared corpus, and documents aimed at the typed fields.
	for _, seed := range append(fuzzSeeds,
		"s = 'x'\ni = 127\nu = 1\nf = 1.5\nb = true\nt = 1979-05-27T07:32:00Z\nld = 1979-05-27\n",
		"arr = [1, 2, 3]\nsl = ['a']\nm = {a = 1.5}\nmk = {1 = true}\n",
		"[p]\ns = 'nested'\n[p.p]\ni = -1\n[[aot]]\nb = false\n[[aot]]\nany = [1]\n",
		"i = 128\nu = -1\nf = 1e39\nmk = {x = true}\n",
	) {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		var out fuzzTarget
		err := toml.New().Unmarshal(doc, &out)
		//: a refusal is always the codec's own.
		if err != nil && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("refusal is not UNMARSHAL_FAILED: %v", err)
		}
	})
}

// sameValue compares two decoded documents, NaN equal to NaN and instants by
// Equal.
func sameValue(a, b any) bool {
	switch x := a.(type) {
	//: a table.
	case map[string]any:
		y, ok := b.(map[string]any)
		//: the same keys, each with the same value.
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, found := y[k]; !found || !sameValue(v, w) {
				return false
			}
		}
		return true
	//: an array.
	case []any:
		y, ok := b.([]any)
		//: the same elements, in order.
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	//: a float, NaN being equal to itself here.
	case float64:
		y, ok := b.(float64)
		return ok && (x == y && math.Signbit(x) == math.Signbit(y) || math.IsNaN(x) && math.IsNaN(y))
	//: an instant, at the same offset.
	case time.Time:
		y, ok := b.(time.Time)
		_, xo := x.Zone()
		_, yo := y.Zone()
		return ok && x.Equal(y) && xo == yo
	//: everything else compares exactly.
	default:
		return reflect.DeepEqual(a, b)
	}
}
