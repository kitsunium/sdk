package kit

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// initSawATest is what the guard said during this package's
// initialisation, before the test main registered any flag: a replacement a
// test declares in a package-level value starts there.
var initSawATest = testBinary()

// The guard is what the linker set when go test built the binary, never a
// run-time signal a product could send — a flag named test.v, a call to
// testing.Init —, and a test's own initialisation already sees it.
func TestATestBinaryIsWhatTheLinkerSays(t *testing.T) {
	if reflect.ValueOf(testBinary).Pointer() != reflect.ValueOf(testing.Testing).Pointer() {
		t.Error("testBinary is not testing.Testing: a product's run-time state could turn it on")
	}
	if !initSawATest {
		t.Error("a test's package initialisation is not known as a test")
	}
}

// A program go test did not build refuses a replacement: neither a
// production binary nor kit dev can carry one. The guard is testing.Testing,
// true in this binary; the test makes it say what it says in theirs.
func TestReplaceIsRefusedOutsideATest(t *testing.T) {
	testBinary = func() bool { return false }
	t.Cleanup(func() { testBinary = testing.Testing })
	svc := NewService("replaced", "A service a production binary would replace.")
	count := svc.Endpoint("GET /count", func(context.Context, EmptyValue) (int, error) { return 1, nil })
	err := NewApp("x", svc).With(InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard),
		Replace(count, func(context.Context, EmptyValue) (int, error) { return 2, nil }),
	).Start(t.Context())
	var de *DiagnosticsError
	if !errors.As(err, &de) || len(de.Diagnostics) != 1 {
		t.Fatalf("Start = %v, want the replacement refused", err)
	}
	d := de.Diagnostics[0]
	if !strings.Contains(d.Message, "kit.Replace replaces replaced/endpoint/GET /count, but this program is not a test") ||
		d.Source == nil || d.Source.File != "internal/kit/replace_internal_test.go" {
		t.Errorf("the refusal: %+v", d)
	}
}
