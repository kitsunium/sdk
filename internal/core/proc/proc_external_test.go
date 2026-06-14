package proc_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
)

// TestResourceString asserts each Resource renders its systemd directive suffix
// and unknown values degrade to "unknown".
func TestResourceString(t *testing.T) {
	t.Parallel()

	type resCase struct {
		res  proc.Resource
		want string
	}
	cases := []resCase{
		{res: proc.ResourceNoFile, want: "nofile"},
		{res: proc.ResourceNProc, want: "nproc"},
		{res: proc.ResourceCore, want: "core"},
		{res: proc.ResourceMemLock, want: "memlock"},
		{res: proc.ResourceUnknown, want: "unknown"},
		{res: proc.Resource(9999), want: "unknown"},
	}

	runCase := func(t *testing.T, tc resCase) {
		t.Helper()
		//: the rendered name must match the directive suffix exactly.
		if got := tc.res.String(); got != tc.want {
			t.Fatalf("Resource(%d).String() = %q, want %q", tc.res, got, tc.want)
		}
	}

	for _, tc := range cases {
		//: subtest per resource isolates a regressed mapping.
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestResourceKnown asserts only defined, non-zero resources report Known.
func TestResourceKnown(t *testing.T) {
	t.Parallel()

	//: the zero value is never a usable resource.
	if proc.ResourceUnknown.Known() {
		t.Fatal("ResourceUnknown.Known() = true, want false")
	}
	//: a defined resource is known.
	if !proc.ResourceNoFile.Known() {
		t.Fatal("ResourceNoFile.Known() = false, want true")
	}
	//: an out-of-range value is not known.
	if proc.Resource(9999).Known() {
		t.Fatal("Resource(9999).Known() = true, want false")
	}
}

// TestExitSuccess asserts Success only for a clean zero-status exit.
func TestExitSuccess(t *testing.T) {
	t.Parallel()

	type exitCase struct {
		name string
		exit proc.ExitValue
		want bool
	}
	cases := []exitCase{
		{name: "clean", exit: proc.ExitValue{Code: 0}, want: true},
		{name: "nonzero", exit: proc.ExitValue{Code: 1}, want: false},
		{name: "signalled", exit: proc.ExitValue{Signaled: true}, want: false},
	}

	runCase := func(t *testing.T, tc exitCase) {
		t.Helper()
		//: only a non-signalled status-zero exit counts as success.
		if got := tc.exit.Success(); got != tc.want {
			t.Fatalf("%s: Success() = %v, want %v", tc.name, got, tc.want)
		}
	}

	for _, tc := range cases {
		//: subtest per outcome keeps a failure attributable.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNotificationFlags asserts the lifecycle accessors read State and tolerate
// a nil map (the absent-field case).
func TestNotificationFlags(t *testing.T) {
	t.Parallel()

	full := proc.NotificationValue{State: map[string]string{
		"READY":     "1",
		"RELOADING": "1",
		"STOPPING":  "1",
		"WATCHDOG":  "1",
	}}
	//: every flag present must read true.
	if !full.Ready() || !full.Reloading() || !full.Stopping() || !full.Watchdog() {
		t.Fatalf("full notification flags not all true: %+v", full)
	}

	var empty proc.NotificationValue
	//: a nil State must not panic and every flag must read false.
	if empty.Ready() || empty.Reloading() || empty.Stopping() || empty.Watchdog() {
		t.Fatalf("empty notification flags not all false: %+v", empty)
	}
}

// TestLimitInfinity asserts the sentinel is the all-ones ceiling.
func TestLimitInfinity(t *testing.T) {
	t.Parallel()

	//: LimitInfinity must be the unsigned all-ones value (RLIM_INFINITY).
	if proc.LimitInfinity != ^uint64(0) {
		t.Fatalf("LimitInfinity = %d, want %d", proc.LimitInfinity, ^uint64(0))
	}
}
