//go:build !race

// The fixtures of the frozen tables' performance contracts (design/sdk.yaml,
// budgets): each builds a table once and returns a lookup on it, a hit and a
// miss in turn, and the gen_perf_test.go kit gen writes beside this file counts
// its allocations against its budget — zero, since a lookup reads the image in
// place and a Dict's value is a view of it. Race off: the allocation lane runs
// it.
package frozen_test

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/frozen"
)

// perfKeys is how many keys every fixture's table holds.
const perfKeys int = 1024

// perfStrings returns perfKeys string keys and as many values.
func perfStrings() ([]string, []string) {
	keys, values := make([]string, perfKeys), make([]string, perfKeys)
	for i := range keys {
		keys[i], values[i] = "key-"+strconv.Itoa(i), "value-"+strconv.Itoa(i)
	}
	return keys, values
}

// perfMapGet is Map.Get's fixture: a present key, then an absent one.
func perfMapGet(tb testing.TB) func() {
	keys, values := make([]uint64, perfKeys), make([]uint32, perfKeys)
	for i := range keys {
		keys[i], values[i] = uint64(i)*2, uint32(i)
	}
	m, err := frozen.NewMap(keys, values)
	if err != nil {
		tb.Fatal(err)
	}
	n := 0
	return func() {
		n++
		m.Get(uint64(n % (2 * perfKeys)))
	}
}

// perfStringMapGet is StringMap.Get's fixture: a present key, then an absent
// one.
func perfStringMapGet(tb testing.TB) func() {
	keys, _ := perfStrings()
	values := make([]uint32, perfKeys)
	m, err := frozen.NewStringMap(keys, values)
	if err != nil {
		tb.Fatal(err)
	}
	probes := append(keys, "absent-0", "absent-1", "key-", "")
	n := 0
	return func() {
		n++
		m.Get(probes[n%len(probes)])
	}
}

// perfDictGet is Dict.Get's fixture: a present key, whose value is returned,
// then an absent one.
func perfDictGet(tb testing.TB) func() {
	keys, values := perfStrings()
	d, err := frozen.NewDict(keys, values)
	if err != nil {
		tb.Fatal(err)
	}
	probes := append(keys, "absent-0", "absent-1", "key-", "")
	n := 0
	return func() {
		n++
		d.Get(probes[n%len(probes)])
	}
}
