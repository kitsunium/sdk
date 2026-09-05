//go:build linux

// Package exec — Linux pre-exec cgroup v2 placement.
package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_validateCgroupPath pins the statfs magic check, which is the whole point
// of validating up front.
//
// A plain directory containing a file literally named cgroup.procs would pass
// any check based on existence or naming — and then the trampoline's pid-write
// would succeed, write into an ordinary file, and the child would run COMPLETELY
// UNCONFINED while Start reported success. Only the filesystem type can tell the
// two apart, so only that check is load-bearing.
func Test_validateCgroupPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	//: the decoy: a normal directory that looks exactly like a control group.
	decoy := filepath.Join(dir, "decoy")
	if err := os.Mkdir(decoy, 0o755); err != nil {
		t.Fatalf("creating the decoy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, cgroupProcsFile), nil, 0o644); err != nil {
		t.Fatalf("seeding the decoy: %v", err)
	}
	plainFile := filepath.Join(dir, "file")
	if err := os.WriteFile(plainFile, nil, 0o644); err != nil {
		t.Fatalf("creating the plain file: %v", err)
	}

	type tc struct {
		name    string
		path    string
		wantErr bool
	}
	tests := []tc{
		{name: "an empty path is a no-op"},
		{name: "a path that does not exist", path: filepath.Join(dir, "absent"), wantErr: true},
		{name: "a regular file", path: plainFile, wantErr: true},
		{name: "an ordinary directory", path: dir, wantErr: true},
		//: the decoy is the case this check exists for.
		{name: "a directory that merely contains cgroup.procs", path: decoy, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := validateCgroupPath(c.path)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeCgroupUnavailable) {
				t.Fatalf("validateCgroupPath(%s) = %v, want CGROUP_UNAVAILABLE", c.name, err)
			}
			//: the offending path rides along, or an operator cannot tell
			//: which of several confinement settings was wrong.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "cgroup_path" && f.StringValue() == c.path {
					named = true
				}
			}
			if !named {
				t.Errorf("the error does not name the path: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("validateCgroupPath(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyCgroupPlacement pins the trampoline half: it runs in the child
// between the rlimit step and execve, so it cannot return an error value — the
// trampoline reports a diagnostic string over the handshake pipe instead.
//
// An empty message means placed. Anything else must name the file, because by
// the time the parent sees it the child is already gone.
func Test_applyCgroupPlacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writable := filepath.Join(dir, "writable")
	if err := os.Mkdir(writable, 0o755); err != nil {
		t.Fatalf("creating the writable directory: %v", err)
	}

	type tc struct {
		name string
		path string
		//: whether the call must report a failure; an empty path and a
		//: writable directory both succeed.
		wantFailure bool
	}
	tests := []tc{
		{name: "an empty path is a no-op"},
		//: a plain directory accepts the write; this half does not re-validate
		//: the filesystem type, which is why the parent check above must.
		{name: "a writable directory", path: writable},
		{name: "a directory that does not exist", path: filepath.Join(dir, "absent"), wantFailure: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := applyCgroupPlacement(c.path)
		if c.wantFailure {
			if got == "" {
				t.Fatalf("applyCgroupPlacement(%s) reported success, want a diagnostic", c.name)
			}
			//: the message has to name the file: the child is gone by the time
			//: the parent reads it, so this string is the only evidence left.
			if !strings.Contains(got, cgroupProcsFile) {
				t.Errorf("the diagnostic %q does not name %s", got, cgroupProcsFile)
			}
			return
		}
		if got != "" {
			t.Fatalf("applyCgroupPlacement(%s) = %q, want success", c.name, got)
		}
		if c.path == "" {
			return
		}
		//: the pid actually landed in the file, which is what moves the
		//: process into the group.
		data, err := os.ReadFile(filepath.Join(c.path, cgroupProcsFile))
		if err != nil {
			t.Fatalf("reading back cgroup.procs: %v", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Error("cgroup.procs is empty after a reported placement")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
