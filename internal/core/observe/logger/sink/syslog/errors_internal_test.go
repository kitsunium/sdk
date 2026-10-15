package syslog

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_exitIOErr pins the EX_IOERR exit status the I/O sentinels carry — the
// design's `exit: IOERR` (ADR 0176).
func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *errs.Error
		want     int
	}{
		{"DialFailed is sysexits EX_IOERR 74", DialFailed, 74},
		{"WriteFailed is sysexits EX_IOERR 74", WriteFailed, 74},
		{"CloseFailed is sysexits EX_IOERR 74", CloseFailed, 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.ExitCode(); got != tc.want {
				t.Errorf("%s: ExitCode() = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}
