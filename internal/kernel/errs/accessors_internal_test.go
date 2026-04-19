package errs

import (
	"context"
	"errors"
	"testing"
)

func Test_deepestError(t *testing.T) {
	t.Parallel()
	sample := Define(3109, "DEEP_TEST", "Deep test public", "deep test private")
	tests := []struct {
		name        string
		in          error
		wantDeepest *Error
	}{
		{"sdk error directly", sample, sample},
		{"wrapped stdlib cause", Wrap(context.Canceled, WrapParams{
			Code: 3111, Reason: "CTX_DEEP", Public: "Context cancelled in deep test", Private: "debug",
		}), nil},
		{"nil cause", nil, nil},
		{"stdlib without sdk layer", errors.New("plain"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deepestError(tc.in)
			if tc.wantDeepest == sample {
				if got == nil || got.Code() != sample.Code() {
					t.Errorf("deepestError returned wrong Error: %+v", got)
				}
				return
			}
			if tc.wantDeepest == nil && tc.name == "wrapped stdlib cause" {
				if got == nil || got.Code() != 3111 {
					t.Errorf("expected wrapping sdk layer as deepest, got %+v", got)
				}
				return
			}
			if got != nil {
				t.Errorf("deepestError = %+v, want nil", got)
			}
		})
	}
}
