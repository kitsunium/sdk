package frozen_test

import (
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/frozen"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sizes are the key counts every property runs at: the empty table, one key,
// the buckets' edge and enough keys for evictions to happen.
var sizes = []int{0, 1, 2, 3, 7, 100, 1000, 50_000}

// intKeys draws n distinct uint64 keys from a seeded source.
func intKeys(n int, seed uint64) map[uint64]uint64 {
	r := rand.New(rand.NewPCG(seed, 1))
	m := make(map[uint64]uint64, n)
	for len(m) < n {
		m[r.Uint64()] = r.Uint64()
	}
	return m
}

// split returns a map's keys and values as two aligned slices.
func split[K comparable, V any](m map[K]V) ([]K, []V) {
	ks, vs := make([]K, 0, len(m)), make([]V, 0, len(m))
	for k, v := range m {
		ks, vs = append(ks, k), append(vs, v)
	}
	return ks, vs
}

func TestMapMatchesNativeMap(t *testing.T) {
	t.Parallel()
	for _, n := range sizes {
		want := intKeys(n, uint64(n))
		m, err := frozen.NewMap(split(want))
		if err != nil {
			t.Fatalf("n=%d: NewMap: %v", n, err)
		}
		if m.Len() != n {
			t.Fatalf("n=%d: Len = %d", n, m.Len())
		}
		for k, v := range want {
			if got, ok := m.Get(k); !ok || got != v {
				t.Fatalf("n=%d: Get(%d) = %d, %v; want %d", n, k, got, ok, v)
			}
		}
		absent := intKeys(1000, uint64(n)+99)
		for k := range absent {
			if _, in := want[k]; in {
				continue
			}
			if _, ok := m.Get(k); ok {
				t.Fatalf("n=%d: Get(%d) found an absent key", n, k)
			}
		}
		seen := 0
		for k, v := range m.All() {
			if want[k] != v {
				t.Fatalf("n=%d: All yielded %d=%d", n, k, v)
			}
			seen++
		}
		if seen != n {
			t.Fatalf("n=%d: All yielded %d keys", n, seen)
		}
	}
}

func TestStringMapAndDictMatchNativeMap(t *testing.T) {
	t.Parallel()
	for _, n := range sizes {
		want := make(map[string]string, n)
		for i := range n {
			want["key/"+strconv.Itoa(i*7919)] = "value-" + strconv.Itoa(i)
		}
		ks, vs := split(want)
		ids := make([]uint32, n)
		for i := range ids {
			ids[i] = uint32(i)
		}
		sm, err := frozen.NewStringMap(ks, ids)
		if err != nil {
			t.Fatalf("n=%d: NewStringMap: %v", n, err)
		}
		d, err := frozen.NewDict(ks, vs)
		if err != nil {
			t.Fatalf("n=%d: NewDict: %v", n, err)
		}
		for i, k := range ks {
			if got, ok := sm.Get(k); !ok || got != uint32(i) {
				t.Fatalf("n=%d: StringMap.Get(%q) = %d, %v", n, k, got, ok)
			}
			if got, ok := d.Get(k); !ok || got != want[k] {
				t.Fatalf("n=%d: Dict.Get(%q) = %q, %v", n, k, got, ok)
			}
		}
		for _, k := range []string{"", "key/", "key/1", "absent", "value-0"} {
			if _, in := want[k]; in {
				continue
			}
			if _, ok := sm.Get(k); ok {
				t.Fatalf("n=%d: StringMap.Get(%q) found an absent key", n, k)
			}
			if _, ok := d.Get(k); ok {
				t.Fatalf("n=%d: Dict.Get(%q) found an absent key", n, k)
			}
		}
		for k, v := range d.All() {
			if want[k] != v {
				t.Fatalf("n=%d: All yielded %q=%q", n, k, v)
			}
		}
	}
}

func TestSignedAndNarrowTypes(t *testing.T) {
	t.Parallel()
	keys := []int8{-128, -1, 0, 1, 127}
	values := []int16{-32768, -2, 0, 2, 32767}
	m, err := frozen.NewMap(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if got, ok := m.Get(k); !ok || got != values[i] {
			t.Fatalf("Get(%d) = %d, %v; want %d", k, got, ok, values[i])
		}
	}
	image, err := m.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	back, err := frozen.LoadMap[int8, int16](image, frozen.CheckAll)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if got, ok := back.Get(k); !ok || got != values[i] {
			t.Fatalf("loaded Get(%d) = %d, %v", k, got, ok)
		}
	}
	if _, err := frozen.LoadMap[uint8, int16](image, frozen.CheckHeader); !errs.HasCode(err, frozen.CodeImageShape) {
		t.Fatalf("loading as unsigned keys: %v, want IMAGE_SHAPE", err)
	}
}

func TestEdgeCases(t *testing.T) {
	t.Parallel()
	if _, err := frozen.NewMap([]uint64{1, 2, 1}, []uint64{1, 2, 3}); !errs.HasCode(err, frozen.CodeDuplicateKey) {
		t.Fatalf("duplicate integer key: %v", err)
	}
	if _, err := frozen.NewDict([]string{"a", "b", "a"}, []string{"1", "2", "3"}); !errs.HasCode(err, frozen.CodeDuplicateKey) {
		t.Fatalf("duplicate string key: %v", err)
	}
	if _, err := frozen.NewStringMap([]string{"a"}, []int{1, 2}); !errs.HasCode(err, frozen.CodeLengthMismatch) {
		t.Fatalf("length mismatch: %v", err)
	}
	huge := string(make([]byte, 1<<20))
	d, err := frozen.NewDict([]string{huge, ""}, []string{"", huge})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get(huge); !ok || v != "" {
		t.Fatal("huge key")
	}
	if v, ok := d.Get(""); !ok || v != huge {
		t.Fatal("empty key")
	}
	var zero frozen.Map[uint32, uint32]
	if _, ok := zero.Get(1); ok || zero.Len() != 0 {
		t.Fatal("zero Map is not empty")
	}
	image, _ := zero.MarshalBinary()
	if m, err := frozen.LoadMap[uint32, uint32](image, frozen.CheckAll); err != nil || m.Len() != 0 {
		t.Fatalf("zero Map's image: %v", err)
	}
}

func TestImageRoundTripAndDeterminism(t *testing.T) {
	t.Parallel()
	want := make(map[string]string)
	for i := range 5000 {
		want[strconv.Itoa(i)] = strconv.Itoa(i * i)
	}
	ks, vs := split(want)
	a, err := frozen.NewDict(ks, vs)
	if err != nil {
		t.Fatal(err)
	}
	//: the same set in reverse order must give the same image.
	for i, j := 0, len(ks)-1; i < j; i, j = i+1, j-1 {
		ks[i], ks[j], vs[i], vs[j] = ks[j], ks[i], vs[j], vs[i]
	}
	b, err := frozen.NewDict(ks, vs)
	if err != nil {
		t.Fatal(err)
	}
	ia, _ := a.MarshalBinary()
	ib, _ := b.AppendBinary(nil)
	if string(ia) != string(ib) {
		t.Fatal("two builds of one set wrote different images")
	}
	for _, check := range []frozen.Check{frozen.CheckHeader, frozen.CheckAll} {
		d, err := frozen.LoadDict(ia, check)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range want {
			if got, ok := d.Get(k); !ok || got != v {
				t.Fatalf("loaded Get(%q) = %q, %v", k, got, ok)
			}
		}
	}
	//: one flipped bit anywhere is refused under CheckAll.
	for _, at := range []int{0, 5, 13, 30, 61, 64, len(ia) / 2, len(ia) - 1} {
		bad := append([]byte(nil), ia...)
		bad[at] ^= 0x10
		if _, err := frozen.LoadDict(bad, frozen.CheckAll); err == nil {
			t.Fatalf("a flipped bit at %d was accepted", at)
		}
	}
	if _, err := frozen.LoadDict(ia[:len(ia)-1], frozen.CheckHeader); !errs.HasCode(err, frozen.CodeImageCorrupt) {
		t.Fatalf("truncated image: %v", err)
	}
}

// TestStructuredKeys builds the key sets real callers have — consecutive ids,
// ids on a stride, ids in the high bits, numbered strings — whose structure a
// weak hash keeps and a search then pays for. Each must build and be found.
func TestStructuredKeys(t *testing.T) {
	t.Parallel()
	const n = 100_000
	sets := map[string]func(i int) uint64{
		"consecutive": func(i int) uint64 { return uint64(i) },
		"stride 4096": func(i int) uint64 { return uint64(i) << 12 },
		"high bits":   func(i int) uint64 { return uint64(i) << 40 },
	}
	for name, key := range sets {
		keys, values := make([]uint64, n), make([]uint32, n)
		for i := range keys {
			keys[i], values[i] = key(i), uint32(i)
		}
		m, err := frozen.NewMap(keys, values)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, k := range keys {
			if v, ok := m.Get(k); !ok || v != uint32(i) {
				t.Fatalf("%s: Get(%d) = %d, %v", name, k, v, ok)
			}
		}
	}
	words, ids := make([]string, n), make([]int32, n)
	for i := range words {
		words[i], ids[i] = "/v1/users/"+strconv.Itoa(i)+"/profile", int32(i)
	}
	sm, err := frozen.NewStringMap(words, ids)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range words {
		if v, ok := sm.Get(w); !ok || v != int32(i) {
			t.Fatalf("Get(%q) = %d, %v", w, v, ok)
		}
	}
}
