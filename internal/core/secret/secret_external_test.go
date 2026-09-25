package secret_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/secret"
)

// TestStoreIsFrozenAtFiveMethods pins the published port's method set. A
// sixth method would break every downstream implementation at compile time
// (ADR 0039); a new capability is a sibling interface instead, and this test
// is what makes adding one to Store a decision rather than an edit.
func TestStoreIsFrozenAtFiveMethods(t *testing.T) {
	t.Parallel()
	storeType := reflect.TypeFor[secret.Store]()
	got := make([]string, 0, storeType.NumMethod())
	for method := range storeType.Methods() {
		got = append(got, method.Name)
	}
	want := []string{"Get", "Names", "Prune", "Put", "Versions"}
	//: reflect lists methods in lexical order, so the comparison is exact.
	if !slices.Equal(got, want) {
		t.Fatalf("Store methods = %v, want %v — widen by a sibling interface, never here", got, want)
	}
}

// TestValueIsNotComparable pins the claim that == does not compile on a
// Value: reflection answers the same question the compiler does.
func TestValueIsNotComparable(t *testing.T) {
	t.Parallel()
	//: comparable types admit ==; a Value must not, or an identity comparison
	//: would read as an equality one.
	if reflect.TypeFor[secret.Value]().Comparable() {
		t.Fatal("secret.Value is comparable: == would compile and compare pointers, not secrets")
	}
}
