package journald_test

import (
	"testing"

	corejournald "github.com/kitsunium/sdk/internal/core/observe/logger/writer/journald"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_sentinelsCarryCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"open sentinel", corejournald.JournaldOpenFailed, corejournald.CodeJournaldOpenFailed},
		{"write sentinel", corejournald.JournaldWriteFailed, corejournald.CodeJournaldWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: each exported sentinel must report its documented dotted-quad code.
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("%s: HasCode(%v, %v) = false", tc.name, tc.err, tc.code)
			}
		})
	}
}
