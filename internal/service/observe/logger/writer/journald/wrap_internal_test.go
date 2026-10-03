package journald

import (
	"errors"
	"testing"

	corejournald "github.com/kitsunium/sdk/internal/core/observe/logger/writer/journald"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_wrapOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cause    error
		wantWrap bool
	}{
		{"nil cause carries the open code", nil, false},
		{"non-nil cause stays Is-matchable", errJournaldBoom{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapOpen(tc.cause)
			//: every wrap must carry the open code.
			if !errs.HasCode(err, corejournald.CodeJournaldOpenFailed) {
				t.Errorf("%s: err=%v want open-failed", tc.name, err)
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
		{"non-nil cause stays Is-matchable", errJournaldBoom{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapWrite(tc.cause, 4)
			//: every wrap must carry the write code.
			if !errs.HasCode(err, corejournald.CodeJournaldWriteFailed) {
				t.Errorf("%s: err=%v want write-failed", tc.name, err)
			}
			//: a non-nil cause must remain reachable via errors.Is.
			if tc.wantWrap && !errors.Is(err, tc.cause) {
				t.Errorf("%s: errors.Is lost the cause", tc.name)
			}
		})
	}
}
