package tlv

import (
	"reflect"
	"testing"
)

// typeinfoSampleStruct exercises both the exported-field collection
// and the unexported-field skip path in buildStructTypeInfo.
type typeinfoSampleStruct struct {
	Name   string
	Age    int
	hidden bool //nolint:unused // intentionally unexported for the test
}

// Test_buildStructTypeInfo covers the metadata builder: exported field
// metadata is captured in declaration order; unexported fields are
// dropped at build time so hot loops never re-check PkgPath.
func Test_buildStructTypeInfo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		typ       reflect.Type
		wantNames []string
	}
	tests := []tc{
		{"sample-struct", reflect.TypeFor[typeinfoSampleStruct](), []string{"Name", "Age"}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		ti := buildStructTypeInfo(tc.typ)
		//: field count must match the exported-only expectation.
		if len(ti.fields) != len(tc.wantNames) {
			t.Fatalf("%s: got %d fields, want %d", tc.name, len(ti.fields), len(tc.wantNames))
		}
		//: per-field name + index sanity.
		for i, want := range tc.wantNames {
			if ti.fields[i].name != want {
				t.Errorf("%s: fields[%d].name=%q want %q", tc.name, i, ti.fields[i].name, want)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_cachedStructTypeInfo covers the memoising wrapper: repeated
// calls on the same reflect.Type must hand back the same pointer.
func Test_cachedStructTypeInfo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		typ  reflect.Type
	}
	tests := []tc{
		{"sample-struct", reflect.TypeFor[typeinfoSampleStruct]()},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		first := cachedStructTypeInfo(tc.typ)
		second := cachedStructTypeInfo(tc.typ)
		//: pointer equality proves the cache hit on the second call.
		if first != second {
			t.Errorf("%s: cachedStructTypeInfo returned distinct pointers across calls", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
