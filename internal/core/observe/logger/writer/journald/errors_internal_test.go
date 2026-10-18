package journald

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_exitIOErr pins the EX_IOERR exit status both sentinels carry — the
// design's `exit: IOERR` (ADR 0176).
func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *errs.Error
		want     int
	}{
		{"JournaldOpenFailed is sysexits EX_IOERR 74", JournaldOpenFailed, 74},
		{"JournaldWriteFailed is sysexits EX_IOERR 74", JournaldWriteFailed, 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the I/O exit code must match the sysexits constant.
			if got := tc.sentinel.ExitCode(); got != tc.want {
				t.Errorf("%s: ExitCode() = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}
