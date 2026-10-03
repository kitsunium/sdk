package profiling_test

import (
	"testing"

	coreprofiling "github.com/kitsunium/sdk/internal/core/observe/profiling"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestEverySentinelCarriesItsCode pins the seven sentinels to their codes and
// the three refusals a caller can act on to their HTTP statuses: a window to
// fix (400), a profiler to wait for (409), a caller that gave up (503). The
// values were allocated to the engine and did not change when their
// declaration moved here (ADR 0160 §3).
func TestEverySentinelCarriesItsCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		err    error
		name   string
		code   errs.Code
		status int
	}
	cases := []tc{
		{name: "window invalid", err: coreprofiling.WindowInvalid, code: 0x00_03_59_01, status: 400},
		{name: "profiler busy", err: coreprofiling.ProfilerBusy, code: 0x00_03_59_02, status: 409},
		{name: "capture canceled", err: coreprofiling.CaptureCanceled, code: 0x00_03_59_03, status: 503},
		{name: "capture failed", err: coreprofiling.CaptureFailed, code: 0x00_03_59_04},
		{name: "profile malformed", err: coreprofiling.ProfileMalformed, code: 0x00_03_59_05},
		{name: "profile too large", err: coreprofiling.ProfileTooLarge, code: 0x00_03_59_06},
		{name: "sample type missing", err: coreprofiling.SampleTypeMissing, code: 0x00_03_59_07},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the value a consumer branches on, literally.
		if !errs.HasCode(c.err, c.code) {
			t.Errorf("%s: HasCode(%v, %v) = false", c.name, c.err, c.code)
		}
		//: only the three actionable refusals pin a status.
		if c.status != 0 && errs.HTTPStatusOf(c.err) != c.status {
			t.Errorf("%s: HTTPStatusOf = %d, want %d", c.name, errs.HTTPStatusOf(c.err), c.status)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
