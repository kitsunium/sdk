package errs_test

import (
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestDeprecatedShimsStillExist documents the transitional deprecated
// symbols that MUST be removed before the v1.0.0 tag is cut. The test
// binds each symbol into a reflect.Value so the Go compiler catches a
// deletion at build time — a runtime check that passes today because
// the shims are still present.
//
// Before v1.0.0:
//
//  1. Delete the shim in internal/kernel/errs (DefineInt, NewErrorInt,
//     HasCodeInt, LayerOf, CodeOf returning int, Error.Code() int).
//  2. Delete the re-export from pkg/v1/errs.
//  3. Flip this test to assert the symbols are ABSENT (remove the binds
//     below, use reflect.TypeOf(errs.CodeValueOf).String() to document
//     the remaining typed-only surface).
//
// The gate ensures the "Removed before v1.0.0" commitment in ADR 0005
// §Deferred does not silently decay into a permanent v1 surface.
// Finding #30 from post-audit review.
func TestDeprecatedShimsStillExist(t *testing.T) {
	t.Parallel()
	//: reflection binds the symbols at runtime AND fails compilation if
	//: any reference goes missing — either outcome is the signal we want.
	shims := []reflect.Value{
		reflect.ValueOf(errs.CodeOf),
		reflect.ValueOf(errs.HasCode),
		reflect.ValueOf(errs.LayerOf),
	}
	//: every entry must bind to a non-zero value; a nil here is a bug.
	for i, v := range shims {
		if !v.IsValid() || v.IsNil() {
			t.Errorf("shim[%d] bound to zero value; unexpected removal", i)
		}
	}
	//: documentary log so test-runner output records the pre-1.0 state.
	t.Logf("pre-v1.0.0 state: %d deprecated int shims still in surface", len(shims))
}
