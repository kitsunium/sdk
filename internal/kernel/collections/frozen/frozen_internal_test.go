package frozen

import (
	"math"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTableHoldsNoPointerPerKey holds the layout's promise: a table's bulk is
// byte slices and one string over them, so whatever its size the collector
// finds a fixed handful of pointers in it and scans none of its bytes.
func TestTableHoldsNoPointerPerKey(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[table]()
	pointers := 0
	for i := range typ.NumField() {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.Uint8 {
				t.Fatalf("field %s is a slice of %s, which may hold pointers", f.Name, f.Type.Elem())
			}
			pointers++
		case reflect.String:
			pointers++
		case reflect.Pointer, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Array:
			t.Fatalf("field %s is a %s", f.Name, f.Type.Kind())
		default:
		}
	}
	if pointers != 6 {
		t.Fatalf("a table holds %d pointers, want 6 (image, pilots, tags, remap, records, blob)", pointers)
	}
}

func TestBuildRefusesWhatAnImageCannotAddress(t *testing.T) {
	t.Parallel()
	never := func(uint64, int) uint64 { t.Fatal("hashed a refused build"); return 0 }
	_, err := build(shape{kind: kindText}, 1, math.MaxUint32+1, never, nil, nil)
	if !errs.HasCode(err, CodeTooLarge) {
		t.Fatalf("a blob past 4 GiB: %v", err)
	}
	//: an int of 32 bits cannot even name that many keys.
	if keys := uint64(maxKeys) + 1; keys <= math.MaxInt {
		_, err = build(shape{kind: kindInts, kw: 8, vw: 8}, int(keys), 0, never, nil, nil)
		if !errs.HasCode(err, CodeTooLarge) {
			t.Fatalf("more keys than slots: %v", err)
		}
	}
}

func TestEveryHashCollidingIsUnsolved(t *testing.T) {
	t.Parallel()
	_, err := build(shape{kind: kindInts, kw: 8, vw: 8}, 4,
		0, func(uint64, int) uint64 { return 42 }, func(int, int) bool { return false }, nil)
	if !errs.HasCode(err, CodeUnsolved) {
		t.Fatalf("four keys of one hash under every seed: %v", err)
	}
}

func TestWidthOf(t *testing.T) {
	t.Parallel()
	check := func(name string, w byte, signed bool, wantW byte, wantSigned bool) {
		if w != wantW || signed != wantSigned {
			t.Errorf("%s: (%d, %v), want (%d, %v)", name, w, signed, wantW, wantSigned)
		}
	}
	w, s := widthOf[int8]()
	check("int8", w, s, 1, true)
	w, s = widthOf[uint16]()
	check("uint16", w, s, 2, false)
	w, s = widthOf[int32]()
	check("int32", w, s, 4, true)
	w, s = widthOf[uint64]()
	check("uint64", w, s, 8, false)
}

// TestSearchEvicts runs the search at a size where buckets do get evicted,
// and checks the plan it returns is a bijection onto [0, n).
func TestSearchEvicts(t *testing.T) {
	t.Parallel()
	const n = 200_000
	p, dup, ok := solve(n, func(seed uint64, i int) uint64 { return hashInt(seed, uint64(i)) }, func(i, j int) bool { return i == j })
	if !ok || dup >= 0 {
		t.Fatalf("solve: ok=%v dup=%d", ok, dup)
	}
	seen := make([]bool, n)
	for i, x := range p.pos {
		if x >= n || seen[x] {
			t.Fatalf("key %d at slot %d, out of range or taken twice", i, x)
		}
		seen[x] = true
	}
}
