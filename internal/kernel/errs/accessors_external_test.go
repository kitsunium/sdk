package errs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func newAccessorsSentinel(tb testing.TB) *errs.Error {
	tb.Helper()
	//: 0x00_03_01_01 = 0.3.1.1 (service/logger WriterNil dotted-quad).
	return errs.Define(0x00_03_01_01, "WRITER_NIL_ACC",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer (accessors test)")
}

func TestCodeOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		in       func(tb testing.TB) error
		wantCode errs.Code
		wantOK   bool
	}
	tests := []tc{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 0x00_03_01_01, true},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, 0, false},
		{"nil", func(tb testing.TB) error { return nil }, 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := errs.CodeOf(c.in(t))
		//: typed Code value AND presence flag must both match.
		if got != c.wantCode || ok != c.wantOK {
			t.Errorf("CodeOf = (%s, %v), want (%s, %v)", got, ok, c.wantCode, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestReasonOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		in         func(tb testing.TB) error
		wantReason string
		wantOK     bool
	}
	tests := []tc{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "WRITER_NIL_ACC", true},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, "", false},
		{"nil", func(tb testing.TB) error { return nil }, "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := errs.ReasonOf(c.in(t))
		//: textual reason + presence flag must round-trip exactly.
		if got != c.wantReason || ok != c.wantOK {
			t.Errorf("ReasonOf = (%q, %v), want (%q, %v)", got, ok, c.wantReason, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPublicOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   func(tb testing.TB) error
		want string
	}
	tests := []tc{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "Log handler requires a non-nil writer"},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, ""},
		{"nil", func(tb testing.TB) error { return nil }, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: wire-safe public string must be returned verbatim.
		if got := errs.PublicOf(c.in(t)); got != c.want {
			t.Errorf("PublicOf = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPrivateOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   func(tb testing.TB) error
		want string
	}
	tests := []tc{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "service/logger.NewTextHandler called with nil io.Writer (accessors test)"},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, ""},
		{"nil", func(tb testing.TB) error { return nil }, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: diagnostic-only private string must be returned verbatim.
		if got := errs.PrivateOf(c.in(t)); got != c.want {
			t.Errorf("PrivateOf = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestFieldsOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantKeys []string
	}
	tests := []tc{
		{"inner cause fields appear before outer wrapper fields", []string{"inner", "outer"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: 0x00_03_01_0A = 0.3.1.10 (CtxCancelled dotted-quad).
		inner := errs.Wrap(context.Canceled, errs.WrapParams{
			Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED_FOF", Public: "Operation aborted due to cancellation",
			Private: "inner debug",
		}, errs.String("inner", "i"))
		outer := errs.Wrap(inner, errs.WrapParams{}, errs.String("outer", "o"))
		got := errs.FieldsOf(outer)
		//: aggregate length must match the inner+outer field count.
		if len(got) != len(c.wantKeys) {
			t.Fatalf("expected %d fields, got %d", len(c.wantKeys), len(got))
		}
		for i, want := range c.wantKeys {
			//: ordering rule — inner fields appear before outer wrapper fields.
			if got[i].Key() != want {
				t.Errorf("idx %d: key = %s, want %s", i, got[i].Key(), want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFieldsOfAbsent covers the absence arm of FieldsOf: a chain carrying no
// *Error layer must yield nil rather than an empty-but-non-nil slice.
func TestFieldsOfAbsent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   func(tb testing.TB) error
	}
	tests := []tc{
		{"stdlib error", func(_ testing.TB) error { return errors.New("plain") }},
		{"nil error", func(_ testing.TB) error { return nil }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: no sdk layer present → the accessor returns a nil slice.
		if got := errs.FieldsOf(c.in(t)); got != nil {
			t.Errorf("FieldsOf = %v, want nil", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestHTTPStatusOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   func(tb testing.TB) error
		want int
	}
	tests := []tc{
		{"sdk default", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 500},
		{"stdlib default", func(tb testing.TB) error { return errors.New("plain") }, 500},
		{"nil default", func(tb testing.TB) error { return nil }, 500},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: default HTTP status must hold for both SDK and non-SDK inputs.
		if got := errs.HTTPStatusOf(c.in(t)); got != c.want {
			t.Errorf("HTTPStatusOf = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestExitCodeOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   func(tb testing.TB) error
		want int
	}
	tests := []tc{
		{"sdk default", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 70},
		{"stdlib default", func(tb testing.TB) error { return errors.New("plain") }, 70},
		{"nil default", func(tb testing.TB) error { return nil }, 70},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: default POSIX exit code must hold for both SDK and non-SDK inputs.
		if got := errs.ExitCodeOf(c.in(t)); got != c.want {
			t.Errorf("ExitCodeOf = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestHasCode(t *testing.T) {
	t.Parallel()
	//: the accessors sentinel is built with CodeWriterNil = 0x00_03_01_01.
	type tc struct {
		name string
		in   func(tb testing.TB) error
		code errs.Code
		want bool
	}
	tests := []tc{
		{"direct match", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 0x00_03_01_01, true},
		{"miss", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 0x00_03_01_02, false},
		{"nil", func(tb testing.TB) error { return nil }, 0x00_03_01_01, false},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, 0x00_03_01_01, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: typed HasCode must follow the deepest-Error semantics.
		if got := errs.HasCode(c.in(t), c.code); got != c.want {
			t.Errorf("HasCode(%s) = %v, want %v", c.code, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestHasReason(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     func(tb testing.TB) error
		reason string
		want   bool
	}
	tests := []tc{
		{"direct match", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "WRITER_NIL_ACC", true},
		{"miss", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "OTHER", false},
		{"nil", func(tb testing.TB) error { return nil }, "ANY", false},
		//: stdlib error in the chain — the AsType walk hits a non-*Error and
		//: must stop without a match (exercises the !ok exhaustion branch).
		{"stdlib has no sdk layer", func(tb testing.TB) error { return errors.New("plain") }, "ANY", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: reason walk must follow the same chain semantics as HasCode.
		if got := errs.HasReason(c.in(t), c.reason); got != c.want {
			t.Errorf("HasReason(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
