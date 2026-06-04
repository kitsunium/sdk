package memory

import (
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// Test_deepCloneAttrs verifies (V37) that deepCloneAttrs detaches nested
// KindGroup payloads from the caller's slice, preserves nil-for-nil, and
// round-trips non-group attributes unchanged.
func Test_deepCloneAttrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		wantNil   bool
		wantValue string
	}{
		{name: "nil stays nil", wantNil: true},
		{name: "nested group child is deep-cloned", wantNil: false, wantValue: "original"},
	}

	//: each deep-clone scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: the nil scenario asserts the zero-state passthrough directly.
			if tc.wantNil {
				//: nil input must yield a nil result so empty records stay faithful.
				if got := deepCloneAttrs(nil); got != nil {
					t.Fatalf("deepCloneAttrs(nil) = %v, want nil", got)
				}
				return
			}

			//: nested is the slice the caller passes into GroupValue.
			nested := []corelogger.AttrValue{{Key: "inner", Value: corelogger.StringValue("original")}}
			attrs := []corelogger.AttrValue{{Key: "grp", Value: corelogger.GroupValue(nested...)}}
			cloned := deepCloneAttrs(attrs)
			//: mutate the caller's nested slice after the clone is taken.
			nested[0] = corelogger.AttrValue{Key: "inner", Value: corelogger.StringValue("mutated")}

			inner := cloned[0].Value.Group()
			//: the cloned group must round-trip to exactly one child attribute.
			if len(inner) != 1 {
				t.Fatalf("cloned group len = %d, want 1", len(inner))
			}
			//: the child must hold the pre-mutation literal, proving the deep copy.
			if v := inner[0].Value.String(); v != tc.wantValue {
				t.Fatalf("cloned group value = %q, want %q", v, tc.wantValue)
			}
		})
	}
}
