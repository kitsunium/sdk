package journald

import (
	"net"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errJournaldBoom is a sentinel transport failure injected by the white-box
// tests to exercise the error-wrapping branches without a real socket.
type errJournaldBoom struct{}

func (errJournaldBoom) Error() string { return "boom" }

// ignoreClose intentionally drops a test-fixture Close error: cleanup runs past
// assertion time, so a close failure must not mask the real result.
func ignoreClose(err error) {
	//: read the parameter so the discard is explicit, not a bare `_ =`.
	if err == nil {
		//: nothing to drop on the happy path.
		return
	}
}

// failingDialer always fails, driving the open-error path deterministically.
func failingDialer(_, _ string) (net.Conn, error) {
	//: a fixed failure exercises the connect-error branch with no real socket.
	return nil, errJournaldBoom{}
}

func Test_journaldFactory_Name(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"reports the canonical key", "journald"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the factory must report the key it registered under.
			if got := (&journaldFactory{}).Name(); string(got) != tc.want {
				t.Errorf("%s: Name()=%q want %q", tc.name, got, tc.want)
			}
		})
	}
}

func Test_journaldFactory_Open(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      any
		fail     bool
		wantNil  bool
		wantCode errs.Code
	}{
		{"wrong config type rejected", "nope", false, true, 0},
		{"dial failure surfaces open code", Config{Dialer: failingDialer}, true, true, CodeJournaldOpenFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink, err := (&journaldFactory{}).Open(tc.cfg)
			//: every error arm yields a nil sink.
			if tc.wantNil && sink != nil {
				t.Fatalf("%s: sink=%v want nil", tc.name, sink)
			}
			//: the dial-failure arm must carry the open code.
			if tc.fail && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("%s: err=%v want code %v", tc.name, err, tc.wantCode)
			}
		})
	}
}
