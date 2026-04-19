package errs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func newAccessorsSentinel(tb testing.TB) *errs.Error {
	tb.Helper()
	return errs.Define(3101, "WRITER_NIL_ACC",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer (accessors test)")
}

func TestCodeOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       func(tb testing.TB) error
		wantCode int
		wantOK   bool
	}{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 3101, true},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, 0, false},
		{"nil", func(tb testing.TB) error { return nil }, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := errs.CodeOf(tc.in(t))
			if got != tc.wantCode || ok != tc.wantOK {
				t.Errorf("CodeOf = (%d, %v), want (%d, %v)", got, ok, tc.wantCode, tc.wantOK)
			}
		})
	}
}

func TestReasonOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		in         func(tb testing.TB) error
		wantReason string
		wantOK     bool
	}{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "WRITER_NIL_ACC", true},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, "", false},
		{"nil", func(tb testing.TB) error { return nil }, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := errs.ReasonOf(tc.in(t))
			if got != tc.wantReason || ok != tc.wantOK {
				t.Errorf("ReasonOf = (%q, %v), want (%q, %v)", got, ok, tc.wantReason, tc.wantOK)
			}
		})
	}
}

func TestPublicOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		want string
	}{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "Log handler requires a non-nil writer"},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, ""},
		{"nil", func(tb testing.TB) error { return nil }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.PublicOf(tc.in(t)); got != tc.want {
				t.Errorf("PublicOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrivateOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		want string
	}{
		{"sdk error", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "service/logger.NewTextHandler called with nil io.Writer (accessors test)"},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, ""},
		{"nil", func(tb testing.TB) error { return nil }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.PrivateOf(tc.in(t)); got != tc.want {
				t.Errorf("PrivateOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFieldsOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"inner cause fields appear before outer wrapper fields"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := errs.Wrap(context.Canceled, errs.WrapParams{
				Code: 3110, Reason: "CTX_CANCELLED_FOF", Public: "Operation aborted due to cancellation",
				Private: "inner debug",
			}, errs.String("inner", "i"))
			outer := errs.Wrap(inner, errs.WrapParams{}, errs.String("outer", "o"))
			got := errs.FieldsOf(outer)
			if len(got) != 2 {
				t.Fatalf("expected 2 fields, got %d", len(got))
			}
			if got[0].Key() != "inner" || got[1].Key() != "outer" {
				t.Errorf("ordering = [%s, %s], want [inner, outer]", got[0].Key(), got[1].Key())
			}
		})
	}
}

func TestLayerOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		want int
	}{
		{"sdk error layer from code", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 3},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, 0},
		{"nil", func(tb testing.TB) error { return nil }, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.LayerOf(tc.in(t)); got != tc.want {
				t.Errorf("LayerOf = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHTTPStatusOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		want int
	}{
		{"sdk default", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 500},
		{"stdlib default", func(tb testing.TB) error { return errors.New("plain") }, 500},
		{"nil default", func(tb testing.TB) error { return nil }, 500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.HTTPStatusOf(tc.in(t)); got != tc.want {
				t.Errorf("HTTPStatusOf = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExitCodeOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		want int
	}{
		{"sdk default", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 70},
		{"stdlib default", func(tb testing.TB) error { return errors.New("plain") }, 70},
		{"nil default", func(tb testing.TB) error { return nil }, 70},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.ExitCodeOf(tc.in(t)); got != tc.want {
				t.Errorf("ExitCodeOf = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHasCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   func(tb testing.TB) error
		code int
		want bool
	}{
		{"direct match", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 3101, true},
		{"miss", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, 3102, false},
		{"nil", func(tb testing.TB) error { return nil }, 3101, false},
		{"stdlib", func(tb testing.TB) error { return errors.New("plain") }, 3101, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.HasCode(tc.in(t), tc.code); got != tc.want {
				t.Errorf("HasCode(%d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestHasReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		in     func(tb testing.TB) error
		reason string
		want   bool
	}{
		{"direct match", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "WRITER_NIL_ACC", true},
		{"miss", func(tb testing.TB) error { return newAccessorsSentinel(tb) }, "OTHER", false},
		{"nil", func(tb testing.TB) error { return nil }, "ANY", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.HasReason(tc.in(t), tc.reason); got != tc.want {
				t.Errorf("HasReason(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}
