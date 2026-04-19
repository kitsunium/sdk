package errs

import (
	"context"
	"errors"
	"testing"
)

func Test_deepestError(t *testing.T) {
	t.Parallel()
	sample := Define(3109, "DEEP_TEST", "Deep test public", "deep test private")
	wrappedStdlib := Wrap(context.Canceled, WrapParams{
		Code: 3111, Reason: "CTX_DEEP", Public: "Context cancelled in deep test", Private: "debug",
	})
	type tc struct {
		name     string
		in       error
		wantCode int // 0 means "want nil *Error"
	}
	tests := []tc{
		{"sdk error directly", sample, 3109},
		{"wrapped stdlib cause", wrappedStdlib, 3111},
		{"nil cause", nil, 0},
		{"stdlib without sdk layer", errors.New("plain"), 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := deepestError(c.in)
		if c.wantCode == 0 {
			if got != nil {
				t.Errorf("deepestError = %+v, want nil", got)
			}
			return
		}
		if got == nil || got.Code() != c.wantCode {
			t.Errorf("deepestError = %+v, want code %d", got, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
