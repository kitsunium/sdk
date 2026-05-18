package errs

import (
	"context"
	"errors"
	"testing"
)

func Test_deepestError(t *testing.T) {
	t.Parallel()
	//: valid dotted-quad codes:
	//: 0x00_03_01_64 = 0.3.1.100 (service/logger, deep-test serial)
	//: 0x00_03_01_65 = 0.3.1.101 (wrapped stdlib cause)
	sample := Define(0x00_03_01_64, "DEEP_TEST", "Deep test public", "deep test private")
	wrappedStdlib := Wrap(context.Canceled, WrapParams{
		Code: 0x00_03_01_65, Reason: "CTX_DEEP", Public: "Context cancelled in deep test", Private: "debug",
	})
	type tc struct {
		name     string
		in       error
		wantCode int // 0 means "want nil *Error"
	}
	tests := []tc{
		{"sdk error directly", sample, int(uint32(0x00_03_01_64))},
		{"wrapped stdlib cause", wrappedStdlib, int(uint32(0x00_03_01_65))},
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
