package logger_test

import (
	"reflect"
	"testing"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// plainError is a non-SDK error used to exercise the WithError fallback path
// without reaching for the banned errors.New / fmt.Errorf constructors.
type plainError struct {
	msg string
}

// Error renders the flat message, satisfying the error interface.
func (e plainError) Error() string {
	//: return the stored message verbatim.
	return e.msg
}

// withErrorCase groups one Test_WithError table row so the shared assertion
// helper takes a single struct rather than six positional parameters.
type withErrorCase struct {
	// name labels the subtest.
	name string
	// err is the input handed to WithError.
	err error
	// wantCode is the expected error.code rendering for SDK errors.
	wantCode string
	// wantMsg is the expected error.message for the plain-error fallback.
	wantMsg string
	// wantTrail asserts the outermost trail frame is present.
	wantTrail bool
	// wantNil asserts WithError yields no Attrs at all.
	wantNil bool
}

// findAttr returns the Attr stored under key together with ok=true, or a zero
// Attr and ok=false when no Attr carries that key.
func findAttr(attrs []logger.Attr, key string) (logger.Attr, bool) {
	//: scan the slice for the first matching key.
	for _, a := range attrs {
		//: compare against the Attr's Key field.
		if a.Key == key {
			return a, true
		}
	}

	return logger.Attr{}, false
}

// hasKey reports whether any Attr in attrs carries key.
func hasKey(attrs []logger.Attr, key string) bool {
	//: delegate to findAttr and discard the value.
	_, ok := findAttr(attrs, key)

	return ok
}

func Test_WithError(t *testing.T) {
	t.Parallel()

	//: a real, registered SDK sentinel (WriterRequired, 1.1.0.1).
	sentinel := logger.WriterRequired

	//: wrapping the sentinel extends the wrap trail with the outer code while
	//: inheriting the cause metadata (origin wins).
	wrapped := kerrs.Wrap(sentinel, kerrs.WrapParams{
		Code:    kerrs.Code(0x01_01_00_02),
		Reason:  "OUTER",
		Public:  "outer view",
		Private: "outer view detail",
	})

	originCode, _ := kerrs.CodeOf(sentinel)

	tests := []withErrorCase{
		{
			name:     "sentinel sdk error explodes into code/reason/public",
			err:      sentinel,
			wantCode: originCode.String(),
		},
		{
			name:      "wrapped chain emits trail codes",
			err:       wrapped,
			wantCode:  originCode.String(),
			wantTrail: true,
		},
		{
			name:    "plain stdlib error falls back to message only",
			err:     plainError{msg: "disk full"},
			wantMsg: "disk full",
		},
		{
			name:    "nil error yields no attrs",
			err:     nil,
			wantNil: true,
		},
	}

	//: drive every case, reading each table field inside the closure.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := logger.WithError(tc.err)

			//: a nil error must produce no Attrs at all.
			if tc.wantNil {
				//: guard the empty expectation.
				if len(got) != 0 {
					t.Fatalf("WithError(nil) = %d attrs, want 0", len(got))
				}

				//: nil case fully checked.
				return
			}

			//: the plain-error branch carries only error.message.
			if tc.wantMsg != "" {
				assertEqualAttr(t, got, "error.message", tc.wantMsg)

				//: fallback case fully checked.
				return
			}

			//: an SDK error must carry the exact origin code Attr.
			assertEqualAttr(t, got, "error.code", tc.wantCode)

			//: SDK errors always carry a reason Attr.
			if !hasKey(got, "error.reason") {
				t.Fatalf("missing error.reason attr, got %v", got)
			}

			//: a wrapped chain must surface at least the outermost trail frame.
			if tc.wantTrail && !hasKey(got, "error.trail.0") {
				t.Fatalf("missing error.trail.0 attr, got %v", got)
			}
		})
	}
}

// assertEqualAttr checks that attrs carries exactly the Attr built from key and
// want, failing t otherwise.
func assertEqualAttr(t *testing.T, attrs []logger.Attr, key, want string) {
	t.Helper()

	got, ok := findAttr(attrs, key)
	//: the expected key must be present.
	if !ok {
		t.Fatalf("missing %q attr, got %v", key, attrs)
	}

	//: the Attr must equal the one built from key and want.
	if !reflect.DeepEqual(got, logger.String(key, want)) {
		t.Fatalf("%q attr = %v, want value %q", key, got, want)
	}
}
