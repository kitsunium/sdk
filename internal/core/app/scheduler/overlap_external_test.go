package scheduler_test

import (
	"testing"

	coresched "github.com/kitsunium/sdk/internal/core/app/scheduler"
)

// TestOverlapPolicyRendersItsName pins the spellings a log line reads, the
// zero's included, and the honest answer for a value the package never mints.
func TestOverlapPolicyRendersItsName(t *testing.T) {
	t.Parallel()
	for policy, want := range map[coresched.OverlapPolicy]string{
		coresched.OverlapSkip: "skip", coresched.OverlapAllow: "allow",
		coresched.OverlapReplace: "replace", 7: "unknown",
	} {
		if got := policy.String(); got != want {
			t.Fatalf("OverlapPolicy(%d).String() = %q, want %q", policy, got, want)
		}
	}
	var zero coresched.EntryValue
	if zero.Overlap != coresched.OverlapSkip {
		t.Fatalf("the zero EntryValue's Overlap is %v, want skip", zero.Overlap)
	}
}
