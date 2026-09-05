// Package rlimit_test — black-box tests for the rlimit service facade.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// nativeRlimit reports whether this build applies resource limits natively.
//
// It is a probe rather than a build tag or a runtime.GOOS list: the package's
// own stub is the authority on whether it degrades, and asking it directly
// cannot drift from the build tags the way a restated GOOS set can. An empty
// limit set is honourable wherever setrlimit exists and refused where it does
// not, which is exactly the distinction being made.
func nativeRlimit() bool {
	return !errs.HasCode(rlimit.PrepareSysProcAttr(nil), coreproc.CodeUnsupportedPlatform)
}

// TestApply drives the rows that hold on any host: an empty set is a no-op
// where rlimits exist, and an unmapped resource is refused before any syscall.
//
// The unmapped row is the one that matters. ResourceUnknown is the zero value,
// which is what a Resource field gets when nobody sets it, so a caller who
// forgot to name the resource must find out immediately rather than have a
// ceiling silently applied to whatever RLIMIT_0 happens to be.
func TestApply(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		pid    int
		limits map[coreproc.Resource]coreproc.LimitValue
		//: the code expected where rlimits are native; off Unix every row
		//: degrades to UNSUPPORTED_PLATFORM instead.
		wantNative errs.Code
	}
	tests := []tc{
		{name: "an empty limit set", limits: map[coreproc.Resource]coreproc.LimitValue{}},
		{name: "a nil limit set"},
		{
			name:       "the zero-value resource",
			limits:     map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceUnknown: {Soft: 1, Hard: 1}},
			wantNative: coreproc.CodeUnknownResource,
		},
		{
			name:       "a resource beyond the enum",
			limits:     map[coreproc.Resource]coreproc.LimitValue{coreproc.Resource(200): {Soft: 1, Hard: 1}},
			wantNative: coreproc.CodeUnknownResource,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := c.wantNative
		//: off Unix the stub short-circuits before the resource table, so every
		//: row — success included — reports the platform sentinel.
		if !nativeRlimit() {
			want = coreproc.CodeUnsupportedPlatform
		}

		err := rlimit.Apply(c.pid, c.limits)
		if want == 0 {
			if err != nil {
				t.Fatalf("Apply(%s) = %v, want nil", c.name, err)
			}
			return
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("Apply(%s) = %v, want code %v", c.name, err, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPrepareSysProcAttr pins the fail-fast validator. It performs no syscall
// and mutates nothing — Go's SysProcAttr carries no rlimit field — so its only
// job is to refuse, at Spec construction time, a Resource this platform cannot
// honour. Discovering that after the fork instead means a child that starts and
// then dies for a reason the parent never sees.
func TestPrepareSysProcAttr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		limits     map[coreproc.Resource]coreproc.LimitValue
		wantNative errs.Code
	}
	tests := []tc{
		{name: "an empty limit set", limits: map[coreproc.Resource]coreproc.LimitValue{}},
		{name: "a nil limit set"},
		{
			name:       "the zero-value resource",
			limits:     map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceUnknown: {Soft: 1, Hard: 1}},
			wantNative: coreproc.CodeUnknownResource,
		},
		{
			//: one bad entry among good ones must still be refused; a partial
			//: acceptance would be worse than none.
			name: "an unmapped resource beside a mapped one",
			limits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile:  {Soft: 1024, Hard: 1024},
				coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
			},
			wantNative: coreproc.CodeUnknownResource,
		},
		{
			name:   "a mapped resource alone",
			limits: map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024}},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := c.wantNative
		if !nativeRlimit() {
			want = coreproc.CodeUnsupportedPlatform
		}

		err := rlimit.PrepareSysProcAttr(c.limits)
		if want == 0 {
			if err != nil {
				t.Fatalf("PrepareSysProcAttr(%s) = %v, want nil", c.name, err)
			}
			return
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("PrepareSysProcAttr(%s) = %v, want code %v", c.name, err, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
