package nettransport

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_wrapDial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cause    error
		wantWrap bool
	}{
		{"nil cause carries the dial code", nil, false},
		{"non-nil cause stays Is-matchable", errNetBoom{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapDial(tc.cause, "tcp")
			//: every wrap must carry the dial code.
			if !errs.HasCode(err, CodeNetTransportDialFailed) {
				t.Errorf("%s: err=%v want dial-failed code", tc.name, err)
			}
			//: a non-nil cause must remain reachable via errors.Is.
			if tc.wantWrap && !errors.Is(err, tc.cause) {
				t.Errorf("%s: errors.Is lost the cause", tc.name)
			}
		})
	}
}

func Test_wrapWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cause    error
		wantWrap bool
	}{
		{"nil cause carries the write code", nil, false},
		{"non-nil cause stays Is-matchable", errNetBoom{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapWrite(tc.cause, "udp", 7)
			//: every wrap must carry the write code.
			if !errs.HasCode(err, CodeNetTransportWriteFailed) {
				t.Errorf("%s: err=%v want write-failed code", tc.name, err)
			}
			//: a non-nil cause must remain reachable via errors.Is.
			if tc.wantWrap && !errors.Is(err, tc.cause) {
				t.Errorf("%s: errors.Is lost the cause", tc.name)
			}
		})
	}
}

func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"sysexits EX_IOERR is 74", 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the I/O exit code must match the sysexits constant.
			if exitIOErr != tc.want {
				t.Errorf("%s: exitIOErr = %d, want %d", tc.name, exitIOErr, tc.want)
			}
		})
	}
}
