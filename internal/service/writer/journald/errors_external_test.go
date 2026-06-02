package journald_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	jd "github.com/kitsunium/sdk/internal/service/writer/journald"
)

func Test_sentinelsCarryCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"open sentinel", jd.JournaldOpenFailed, jd.CodeJournaldOpenFailed},
		{"write sentinel", jd.JournaldWriteFailed, jd.CodeJournaldWriteFailed},
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
