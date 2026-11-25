package frozen_test

import (
	"hash/crc32"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/frozen"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// seedImages returns one small image of each kind, the fuzzer's seeds.
func seedImages(tb testing.TB) [][]byte {
	keys, values := make([]string, 40), make([]string, 40)
	ints, ids := make([]int64, 40), make([]uint16, 40)
	for i := range keys {
		keys[i], values[i] = "k"+strconv.Itoa(i), "v"+strconv.Itoa(i*i)
		ints[i], ids[i] = int64(i)*-977, uint16(i)
	}
	m, err := frozen.NewMap(ints, ids)
	if err != nil {
		tb.Fatal(err)
	}
	sm, err := frozen.NewStringMap(keys, ids)
	if err != nil {
		tb.Fatal(err)
	}
	d, err := frozen.NewDict(keys, values)
	if err != nil {
		tb.Fatal(err)
	}
	a, _ := m.MarshalBinary()
	b, _ := sm.MarshalBinary()
	c, _ := d.MarshalBinary()
	return [][]byte{a, b, c}
}

// FuzzLoad feeds arbitrary bytes to the three loaders under CheckAll: none may
// panic, and a table one accepts must find every key it yields, with the value
// it yields.
func FuzzLoad(f *testing.F) {
	for _, image := range seedImages(f) {
		f.Add(image)
	}
	f.Fuzz(func(t *testing.T, image []byte) {
		if m, err := frozen.LoadMap[int64, uint16](image, frozen.CheckAll); err == nil {
			for k, v := range m.All() {
				if got, ok := m.Get(k); !ok || got != v {
					t.Fatalf("Map: Get(%d) = %d, %v; All said %d", k, got, ok, v)
				}
			}
			m.Get(12345)
		}
		if m, err := frozen.LoadStringMap[uint16](image, frozen.CheckAll); err == nil {
			for k, v := range m.All() {
				if got, ok := m.Get(k); !ok || got != v {
					t.Fatalf("StringMap: Get(%q) = %d, %v; All said %d", k, got, ok, v)
				}
			}
			m.Get("absent")
		}
		if d, err := frozen.LoadDict(image, frozen.CheckAll); err == nil {
			for k, v := range d.All() {
				if got, ok := d.Get(k); !ok || got != v {
					t.Fatalf("Dict: Get(%q) = %q, %v; All said %q", k, got, ok, v)
				}
			}
			d.Get("absent")
		}
	})
}

// FuzzLoadSealed is FuzzLoad past the checksums: it seals both CRCs over the
// fuzzed bytes first, so the mutations reach the sizes and the walk of
// CheckAll rather than stopping at a checksum.
func FuzzLoadSealed(f *testing.F) {
	for _, image := range seedImages(f) {
		f.Add(image)
	}
	f.Fuzz(func(t *testing.T, image []byte) {
		if len(image) >= 64 {
			sum := crc32.Checksum(image[64:], crc32.MakeTable(crc32.Castagnoli))
			image[48], image[49], image[50], image[51] = byte(sum), byte(sum>>8), byte(sum>>16), byte(sum>>24)
			reseal(image)
		}
		if m, err := frozen.LoadMap[int64, uint16](image, frozen.CheckAll); err == nil {
			for k, v := range m.All() {
				if got, ok := m.Get(k); !ok || got != v {
					t.Fatalf("Map: Get(%d) = %d, %v; All said %d", k, got, ok, v)
				}
			}
			m.Get(-1)
		}
		if m, err := frozen.LoadStringMap[uint16](image, frozen.CheckAll); err == nil {
			for k := range m.All() {
				m.Get(k)
			}
			m.Get("")
		}
		if d, err := frozen.LoadDict(image, frozen.CheckAll); err == nil {
			for k, v := range d.All() {
				if got, ok := d.Get(k); !ok || got != v {
					t.Fatalf("Dict: Get(%q) = %q, %v; All said %q", k, got, ok, v)
				}
			}
			d.Get("")
		}
	})
}

// reseal rewrites an image's header CRC after a test changed its header.
func reseal(image []byte) {
	sum := crc32.Checksum(image[:60], crc32.MakeTable(crc32.Castagnoli))
	image[60], image[61], image[62], image[63] = byte(sum), byte(sum>>8), byte(sum>>16), byte(sum>>24)
}

func TestLoadRefusesEachHeaderFault(t *testing.T) {
	t.Parallel()
	image := seedImages(t)[2]
	cases := []struct {
		name  string
		edit  func([]byte) []byte
		code  errs.Code
		check frozen.Check
	}{
		{"empty", func([]byte) []byte { return nil }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"magic", func(b []byte) []byte { b[0] = 'X'; return b }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"header checksum", func(b []byte) []byte { b[12]++; return b }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"version", func(b []byte) []byte { b[4] = 9; reseal(b); return b }, frozen.CodeImageVersion, frozen.CheckHeader},
		{"reserved", func(b []byte) []byte { b[55] = 1; reseal(b); return b }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"key count", func(b []byte) []byte { b[12]++; reseal(b); return b }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"trailing byte", func(b []byte) []byte { return append(b, 0) }, frozen.CodeImageCorrupt, frozen.CheckHeader},
		{"body under CheckAll", func(b []byte) []byte { b[len(b)-1] ^= 1; return b }, frozen.CodeImageCorrupt, frozen.CheckAll},
	}
	for _, c := range cases {
		bad := c.edit(append([]byte(nil), image...))
		if _, err := frozen.LoadDict(bad, c.check); !errs.HasCode(err, c.code) {
			t.Errorf("%s: %v, want code %v", c.name, err, c.code)
		}
	}
	//: a body damaged after the header was sealed passes CheckHeader, as its
	//: doc says.
	bad := append([]byte(nil), image...)
	bad[len(bad)-1] ^= 1
	if _, err := frozen.LoadDict(bad, frozen.CheckHeader); err != nil {
		t.Fatalf("CheckHeader read the body: %v", err)
	}
	if _, err := frozen.LoadStringMap[uint16](image, frozen.CheckHeader); !errs.HasCode(err, frozen.CodeImageShape) {
		t.Fatalf("a Dict loaded as a StringMap: %v", err)
	}
}

// TestCheckAllRecomputesTheSlots forges an image whose checksums hold but
// whose records are swapped: only the walk of CheckAll can refuse it.
func TestCheckAllRecomputesTheSlots(t *testing.T) {
	t.Parallel()
	m, err := frozen.NewMap([]uint64{1, 2, 3, 4}, []uint64{10, 20, 30, 40})
	if err != nil {
		t.Fatal(err)
	}
	image, _ := m.MarshalBinary()
	const stride = 16
	recs := len(image) - 4*stride
	first, second := image[recs:recs+stride], image[recs+stride:recs+2*stride]
	tmp := append([]byte(nil), first...)
	copy(first, second)
	copy(second, tmp)
	sum := crc32.Checksum(image[64:], crc32.MakeTable(crc32.Castagnoli))
	image[48], image[49], image[50], image[51] = byte(sum), byte(sum>>8), byte(sum>>16), byte(sum>>24)
	reseal(image)
	if _, err := frozen.LoadMap[uint64, uint64](image, frozen.CheckAll); !errs.HasCode(err, frozen.CodeImageCorrupt) {
		t.Fatalf("swapped records: %v", err)
	}
}

func TestConcurrentGets(t *testing.T) {
	t.Parallel()
	keys, values := make([]string, 5000), make([]string, 5000)
	for i := range keys {
		keys[i], values[i] = "k"+strconv.Itoa(i), strconv.Itoa(i)
	}
	d, err := frozen.NewDict(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for g := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := g; i < len(keys); i += 8 {
				if v, ok := d.Get(keys[i]); !ok || v != values[i] {
					t.Errorf("Get(%q) = %q, %v", keys[i], v, ok)
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}
