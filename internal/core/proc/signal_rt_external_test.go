package proc_test

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// realtimeRange is the platform's realtime signal range as #942 names it, or
// ok false where none is named.
func realtimeRange() (lo, hi int, ok bool) {
	switch runtime.GOOS {
	case "linux":
		return 34, 64, true
	case "freebsd":
		return 65, 126, true
	default:
		return 0, 0, false
	}
}

// TestRealtimeSignals pins #942: on Linux and FreeBSD every realtime signal
// round-trips through its SIGRTMIN+n name, its SIGRTMAX-n name and its number;
// elsewhere the same spellings stay UNKNOWN_SIGNAL and the number "signal n".
func TestRealtimeSignals(t *testing.T) {
	t.Parallel()
	lo, hi, named := realtimeRange()
	//: no range: the refusal stays honest.
	if !named {
		for _, spelling := range []string{"SIGRTMIN+3", "RTMIN", "SIGRTMAX-1", "37"} {
			if _, err := proc.Parse(spelling); !errs.HasCode(err, proc.CodeUnknownSignal) {
				t.Errorf("Parse(%q) on %s = %v, want UNKNOWN_SIGNAL", spelling, runtime.GOOS, err)
			}
		}
		if got := proc.Signal(37).String(); got != "signal 37" {
			t.Errorf("Signal(37).String() on %s = %q, want %q", runtime.GOOS, got, "signal 37")
		}
		return
	}
	//: the issue's own example on Linux.
	if runtime.GOOS == "linux" {
		if sig, err := proc.Parse("SIGRTMIN+3"); err != nil || sig != proc.Signal(37) || sig.String() != "SIGRTMIN+3" {
			t.Fatalf("Parse(SIGRTMIN+3) = %v %v, want Signal(37) named SIGRTMIN+3", sig, err)
		}
	}
	//: every signal of the range, by every spelling.
	for n := lo; n <= hi; n++ {
		want := proc.Signal(n)
		name := "SIGRTMIN+" + strconv.Itoa(n-lo)
		if n == lo {
			name = "SIGRTMIN"
		}
		if got := want.String(); got != name || !want.Known() {
			t.Fatalf("Signal(%d).String() = %q (known %v), want %q", n, got, want.Known(), name)
		}
		for _, spelling := range []string{name, "SIGRTMAX-" + strconv.Itoa(hi-n), strconv.Itoa(n), "rtmin+" + strconv.Itoa(n-lo)} {
			if got, err := proc.Parse(spelling); err != nil || got != want {
				t.Fatalf("Parse(%q) = %v %v, want %d", spelling, got, err, n)
			}
		}
	}
	//: SIGRTMAX alone, and the spellings just outside the range or malformed.
	if got, err := proc.Parse("SIGRTMAX"); err != nil || got != proc.Signal(hi) {
		t.Fatalf("Parse(SIGRTMAX) = %v %v, want %d", got, err, hi)
	}
	for _, bad := range []string{"SIGRTMIN-1", "SIGRTMAX+1", "SIGRTMIN+" + strconv.Itoa(hi-lo+1), "SIGRTMIN+x", "SIGRTMIN+-1", "SIGRTMIN+03", strconv.Itoa(hi + 1)} {
		if _, err := proc.Parse(bad); !errs.HasCode(err, proc.CodeUnknownSignal) {
			t.Errorf("Parse(%q) = %v, want UNKNOWN_SIGNAL", bad, err)
		}
	}
}
