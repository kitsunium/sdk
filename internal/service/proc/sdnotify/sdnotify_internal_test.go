// Package sdnotify — white-box tests for the pure parsing/encoding helpers.
package sdnotify

import (
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// resolveCase is one resolveAddr expectation.
type resolveCase struct {
	name string
	raw  string
	want string
}

// parseCase is one parsePayload expectation.
type parseCase struct {
	name      string
	body      string
	wantReady bool
	wantPID   int
	wantStat  string
	wantErr   bool
}

// watchdogCase is one watchdogInterval expectation.
type watchdogCase struct {
	name    string
	raw     string
	wantDur time.Duration
	wantOK  bool
}

// TestResolveAddr verifies the abstract-namespace marker rewrite.
func TestResolveAddr(t *testing.T) {
	t.Parallel()
	//: each case maps a raw NOTIFY_SOCKET value to its bound address form.
	cases := []resolveCase{
		//: a pathname socket passes through unchanged.
		{name: "pathname", raw: "/run/notify", want: "/run/notify"},
		//: a leading '@' becomes a NUL byte for the abstract namespace.
		{name: "abstract", raw: "@app/notify", want: "\x00app/notify"},
		//: an empty string stays empty.
		{name: "empty", raw: "", want: ""},
	}
	//: runCase exercises one resolveAddr expectation.
	runCase := func(t *testing.T, tc resolveCase) {
		t.Helper()
		//: compute the resolved address.
		got := resolveAddr(tc.raw)
		//: the result must equal the expected sun_path form.
		if got != tc.want {
			//: report the mismatch.
			t.Fatalf("resolveAddr(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	//: drive every case as a parallel subtest.
	for _, tc := range cases {
		//: run this case under its own name.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: delegate to the shared helper.
			runCase(t, tc)
		})
	}
}

// TestParsePayload verifies datagram-body parsing into a NotificationValue and
// the InvalidNotification path.
func TestParsePayload(t *testing.T) {
	t.Parallel()
	//: each case maps a datagram body to expected flags/fields or an error.
	cases := []parseCase{
		//: a READY datagram sets the ready flag.
		{name: "ready", body: "READY=1\n", wantReady: true},
		//: STATUS and MAINPID are lifted into typed fields.
		{name: "status_mainpid", body: "STATUS=up\nMAINPID=42\n", wantStat: "up", wantPID: 42},
		//: an empty body parses to an empty (not-ready) notification.
		{name: "empty", body: "", wantReady: false},
		//: a '=' inside the value is preserved (only the first splits).
		{name: "value_with_eq", body: "STATUS=a=b\n", wantStat: "a=b"},
		//: a line without '=' is malformed.
		{name: "no_eq", body: "READY\n", wantErr: true},
		//: a non-integer MAINPID is malformed.
		{name: "bad_mainpid", body: "MAINPID=abc\n", wantErr: true},
	}
	//: runCase exercises one parsePayload expectation.
	runCase := func(t *testing.T, tc parseCase) {
		t.Helper()
		//: parse the body.
		got, err := parsePayload(tc.body)
		//: the error branch asserts the typed InvalidNotification.
		if tc.wantErr {
			//: a malformed body must yield InvalidNotification.
			if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
				//: report the wrong/absent error.
				t.Fatalf("parsePayload(%q) err = %v, want InvalidNotification", tc.body, err)
			}
			//: the error case is fully checked.
			return
		}
		//: the success branch must not error.
		if err != nil {
			//: report the unexpected error.
			t.Fatalf("parsePayload(%q) unexpected err = %v", tc.body, err)
		}
		//: the ready flag must match.
		if got.Ready() != tc.wantReady {
			//: report the wrong ready state.
			t.Fatalf("Ready() = %v, want %v", got.Ready(), tc.wantReady)
		}
		//: the lifted MAINPID must match.
		if got.MainPID != tc.wantPID {
			//: report the wrong MainPID.
			t.Fatalf("MainPID = %d, want %d", got.MainPID, tc.wantPID)
		}
		//: the lifted STATUS must match.
		if got.Status != tc.wantStat {
			//: report the wrong Status.
			t.Fatalf("Status = %q, want %q", got.Status, tc.wantStat)
		}
	}
	//: drive every case as a parallel subtest.
	for _, tc := range cases {
		//: run this case under its own name.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: delegate to the shared helper.
			runCase(t, tc)
		})
	}
}

// TestWatchdogInterval verifies $WATCHDOG_USEC microsecond parsing.
func TestWatchdogInterval(t *testing.T) {
	t.Parallel()
	//: each case maps a raw WATCHDOG_USEC value to a duration and ok flag.
	cases := []watchdogCase{
		//: a positive integer scales microseconds to a Duration.
		{name: "valid", raw: "30000000", wantDur: 30 * time.Second, wantOK: true},
		//: an unset value disables the watchdog.
		{name: "unset", raw: "", wantOK: false},
		//: a non-integer value disables the watchdog.
		{name: "garbage", raw: "abc", wantOK: false},
		//: a zero value disables the watchdog.
		{name: "zero", raw: "0", wantOK: false},
	}
	//: runCase exercises one watchdogInterval expectation.
	runCase := func(t *testing.T, tc watchdogCase) {
		t.Helper()
		//: parse the raw microsecond value.
		d, ok := watchdogInterval(tc.raw)
		//: the ok flag must match.
		if ok != tc.wantOK {
			//: report the wrong ok state.
			t.Fatalf("watchdogInterval(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
		}
		//: when ok, the duration must match.
		if ok && d != tc.wantDur {
			//: report the wrong duration.
			t.Fatalf("watchdogInterval(%q) = %v, want %v", tc.raw, d, tc.wantDur)
		}
	}
	//: drive every case as a parallel subtest.
	for _, tc := range cases {
		//: run this case under its own name.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: delegate to the shared helper.
			runCase(t, tc)
		})
	}
}
