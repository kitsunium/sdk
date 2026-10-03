package logger

import (
	"testing"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// internalError is a non-SDK error used to drive the unexported helpers without
// the banned errors.New / fmt.Errorf constructors.
type internalError struct {
	msg string
}

// Error renders the stored message, satisfying the error interface.
func (e internalError) Error() string {
	//: return the message verbatim.
	return e.msg
}

// keyPresent reports whether any Attr carries key (white-box helper).
func keyPresent(attrs []Attr, key string) bool {
	//: scan for the first matching key.
	for _, a := range attrs {
		//: match on the Attr's Key field.
		if a.Key == key {
			return true
		}
	}

	return false
}

func Test_fallbackAttrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
	}{
		{
			name: "non-empty message",
			msg:  "boom",
		},
		{
			name: "empty message",
			msg:  "",
		},
	}

	//: each case renders one plain error into the fallback Attr slice.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := fallbackAttrs(internalError{msg: tc.msg})

			//: the fallback must emit exactly one Attr.
			if len(got) != 1 {
				t.Fatalf("fallbackAttrs len = %d, want 1", len(got))
			}

			//: that Attr must be keyed error.message.
			if !keyPresent(got, errorMessageKey) {
				t.Fatalf("fallbackAttrs missing %q, got %v", errorMessageKey, got)
			}
		})
	}
}

func Test_appendReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "sdk error contributes a reason attr",
			err:  WriterRequired,
			want: 1,
		},
		{
			name: "plain error contributes nothing",
			err:  internalError{msg: "x"},
			want: 0,
		},
	}

	//: each case appends onto a fresh empty slice.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := appendReason(nil, tc.err)

			//: the count reflects whether a reason was emitted.
			if len(got) != tc.want {
				t.Fatalf("appendReason produced %d attrs, want %d", len(got), tc.want)
			}
		})
	}
}

func Test_appendOptional(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  int
	}{
		{
			name:  "empty value adds no attr",
			value: "",
			want:  0,
		},
		{
			name:  "non-empty value adds one attr",
			value: "BOOM",
			want:  1,
		},
	}

	//: each case appends onto a fresh empty slice.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := appendOptional(nil, errorReasonKey, tc.value)

			//: the count reflects whether the value was kept.
			if len(got) != tc.want {
				t.Fatalf("appendOptional produced %d attrs, want %d", len(got), tc.want)
			}
		})
	}
}

func Test_appendTrail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		trail []kerrs.Code
		want  int
	}{
		{
			name:  "empty trail adds no attrs",
			trail: nil,
			want:  0,
		},
		{
			name:  "two codes add two indexed attrs",
			trail: []kerrs.Code{kerrs.Code(0x01_01_00_01), kerrs.Code(0x01_01_00_02)},
			want:  2,
		},
	}

	//: each case appends onto a fresh empty slice.
	for _, tc := range tests {
		//: parent is parallel, so each subtest is too.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := appendTrail(nil, tc.trail)

			//: the count of emitted attrs must match the trail length.
			if len(got) != tc.want {
				t.Fatalf("appendTrail produced %d attrs, want %d", len(got), tc.want)
			}

			//: a populated trail must index keys from zero upward.
			if tc.want > 0 {
				//: first frame keyed error.trail.0.
				if !keyPresent(got, errorTrailKey+".0") {
					t.Fatalf("missing error.trail.0, got %v", got)
				}
			}
		})
	}
}
