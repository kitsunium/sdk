// Package sdnotify — white-box tests for the pure parsing/encoding helpers.
package sdnotify

import (
	"strconv"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_resolveAddr verifies the abstract-namespace marker rewrite.
func Test_resolveAddr(t *testing.T) {
	t.Parallel()
	//: each case maps a raw NOTIFY_SOCKET value to its bound address form.
	type resolveCase struct {
		name string
		raw  string
		want string
	}
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

// Test_parsePayload verifies datagram-body parsing into a NotificationValue and
// the InvalidNotification path.
func Test_parsePayload(t *testing.T) {
	t.Parallel()
	//: each case maps a datagram body to expected flags/fields or an error.
	type parseCase struct {
		name      string
		body      string
		wantReady bool
		wantPID   int
		wantStat  string
		wantErr   bool
	}
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

// Test_encodePayload verifies encodePayload rejects names/values that
// would forge extra fields via the '\n' / '=' delimiters, and accepts clean
// state. A newline in a value previously injected additional NAME=value lines
// (e.g. a forged READY=1), so such input must surface InvalidNotification.
func Test_encodePayload(t *testing.T) {
	t.Parallel()
	//: each case maps a state map to "encodes cleanly" or "rejected as malformed".
	type encodeCase struct {
		name    string
		state   map[string]string
		wantErr bool
	}
	cases := []encodeCase{
		//: a clean single-line value encodes without error.
		{name: "clean", state: map[string]string{"STATUS": "serving"}},
		//: a value carrying '\n' would inject a forged extra field.
		{name: "value_newline", state: map[string]string{"STATUS": "up\nREADY=1"}, wantErr: true},
		//: a value carrying a trailing '\n' is still field injection.
		{name: "value_trailing_newline", state: map[string]string{"STATUS": "up\n"}, wantErr: true},
		//: a name carrying '\n' would split into a forged field.
		{name: "name_newline", state: map[string]string{"BAD\nREADY": "1"}, wantErr: true},
		//: a name carrying '=' would forge the name/value boundary.
		{name: "name_eq", state: map[string]string{"A=B": "1"}, wantErr: true},
	}
	//: runCase exercises one encodePayload expectation.
	runCase := func(t *testing.T, tc encodeCase) {
		t.Helper()
		//: encode the state map.
		_, err := encodePayload(tc.state)
		//: the error branch asserts the typed InvalidNotification.
		if tc.wantErr {
			//: an injecting name/value must yield InvalidNotification.
			if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
				//: report the wrong/absent error.
				t.Fatalf("encodePayload(%v) err = %v, want InvalidNotification", tc.state, err)
			}
			//: the error case is fully checked.
			return
		}
		//: the success branch must not error.
		if err != nil {
			//: report the unexpected error.
			t.Fatalf("encodePayload(%v) unexpected err = %v", tc.state, err)
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

// Test_watchdogInterval verifies $WATCHDOG_USEC microsecond parsing.
func Test_watchdogInterval(t *testing.T) {
	t.Parallel()
	//: each case maps a raw WATCHDOG_USEC value to a duration and ok flag.
	type watchdogCase struct {
		name    string
		raw     string
		wantDur time.Duration
		wantOK  bool
	}
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

// Test_wrapInvalid pins the malformed-notification wrapper. It carries
// EX_DATAERR rather than EX_OSERR because nothing went wrong with the socket:
// the datagram arrived intact and said something the protocol does not allow,
// and those two failures need different fixes.
func Test_wrapInvalid(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		cause error
		field errs.FieldValue
	}
	tests := []tc{
		{"a line with no separator", nil, errs.String("line", "READY")},
		{"a non-numeric main pid", strconv.ErrSyntax, errs.String("mainpid", "abc")},
		{"an injecting field name", nil, errs.String("name", "A\nB")},
		{"a truncated datagram", nil, errs.String("reason", "datagram truncated")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapInvalid(c.cause, c.field)

		if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
			t.Fatalf("wrapInvalid = %v, want INVALID_NOTIFICATION", err)
		}
		//: EX_DATAERR: the data was wrong, not the operating system.
		if got := errs.ExitCodeOf(err); got != exitDataErr {
			t.Errorf("exit code = %d, want %d", got, exitDataErr)
		}
		//: the offending field rides along, or "malformed datagram" says
		//: nothing about which part of it was.
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == c.field.Key() {
				named = true
			}
		}
		if !named {
			t.Errorf("the error does not carry the %q field: %v", c.field.Key(), errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_mainPIDFromState pins the lift into the typed field. An ABSENT MAINPID is
// valid and yields zero; a PRESENT but unparseable one makes the whole datagram
// malformed — because a supervisor acts on that number, and acting on a
// misparsed one means tracking the wrong process.
func Test_mainPIDFromState(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		state   map[string]string
		want    int
		wantErr bool
	}
	tests := []tc{
		{name: "no main pid at all", state: map[string]string{}},
		{name: "other fields only", state: map[string]string{"READY": "1"}},
		{name: "a plausible pid", state: map[string]string{"MAINPID": "4242"}, want: 4242},
		{name: "pid one", state: map[string]string{"MAINPID": "1"}, want: 1},
		{name: "a non-numeric pid", state: map[string]string{"MAINPID": "abc"}, wantErr: true},
		{name: "an empty pid", state: map[string]string{"MAINPID": ""}, wantErr: true},
		{name: "a float pid", state: map[string]string{"MAINPID": "42.0"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := mainPIDFromState(c.state)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
				t.Fatalf("mainPIDFromState(%v) = %v, want INVALID_NOTIFICATION", c.state, err)
			}
			//: a refused pid must report zero, not a half-parsed number.
			if got != 0 {
				t.Errorf("mainPIDFromState returned %d beside the error", got)
			}
			return
		}
		if err != nil {
			t.Fatalf("mainPIDFromState(%v) = %v, want nil", c.state, err)
		}
		if got != c.want {
			t.Errorf("mainPIDFromState(%v) = %d, want %d", c.state, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
