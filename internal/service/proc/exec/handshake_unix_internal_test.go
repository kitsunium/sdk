//go:build unix

// Package exec — the parent↔trampoline status pipe.
//
// The pipe exists to close a gap nothing else can: a trampoline child that fails
// to apply a limit, or fails to execve, exits with SOME status — and Start cannot
// tell that apart from the real target exiting with the same one. The one-byte
// report is the only evidence, so every path through it is pinned here.
package exec

import (
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_newHandshake pins that both ends come back usable and distinct. A pipe
// with one end unset would make every spawn report success, since await reads
// EOF immediately.
func Test_newHandshake(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		count int
	}
	tests := []tc{
		{"a single pipe", 1},
		{"several pipes", 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for range c.count {
			hs, err := newHandshake()
			if err != nil {
				t.Fatalf("newHandshake = %v, want nil", err)
			}
			if hs == nil || hs.read == nil || hs.write == nil {
				t.Fatalf("newHandshake returned an incomplete pipe: %+v", hs)
			}
			//: the child inherits the write end, so the two must be distinct
			//: descriptors or closing one would close the other.
			if hs.read.Fd() == hs.write.Fd() {
				t.Errorf("both ends share fd %d", hs.read.Fd())
			}
			hs.closeBoth()
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handshake_childFile pins that the file handed to os.StartProcess is the
// WRITE end. Handing over the read end would give the child something it can
// never report through, and the parent would block forever waiting on a pipe
// whose only writer it still holds.
func Test_handshake_childFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the inherited end"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hs, err := newHandshake()
		if err != nil {
			t.Fatalf("newHandshake = %v, want nil", err)
		}
		defer hs.closeBoth()

		got := hs.childFile()
		if got != hs.write {
			t.Errorf("childFile() handed over the wrong end")
		}
		//: it must be writable, which is what the child needs it for.
		if _, werr := got.Write([]byte{handshakeApplyFail}); werr != nil {
			t.Errorf("the child's end is not writable: %v", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handshake_await pins the whole protocol: EOF means the target is running,
// and any byte maps to the sentinel for what went wrong before it could.
//
// The EOF-means-success direction is the subtle one. It works because the write
// end is close-on-exec: a successful execve closes it, and the parent — having
// dropped its own copy first — sees the pipe end. That ordering is what await
// encodes, and getting it backwards would make every spawn look like a failure.
func Test_handshake_await(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the byte the child writes before exiting; empty means it exec'd.
		report   []byte
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a clean execve reports nothing"},
		{name: "a failed limit application", report: []byte{handshakeApplyFail}, wantCode: coreproc.CodeRlimitFailed},
		{name: "a failed execve", report: []byte{handshakeExecFail}, wantCode: coreproc.CodeSpawnFailed},
		{name: "a failed cgroup placement", report: []byte{handshakeCgroupFail}, wantCode: coreproc.CodeCgroupWriteFailed},
		//: an unrecognised byte still means the target never ran, so it must
		//: not be mistaken for success.
		{name: "an unrecognised status byte", report: []byte{'?'}, wantCode: coreproc.CodeSpawnFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hs, err := newHandshake()
		if err != nil {
			t.Fatalf("newHandshake = %v, want nil", err)
		}
		//: stand in for the child: write the status (if any), then close the
		//: inherited end exactly as an exec or an exit would.
		child := hs.childFile()
		if len(c.report) > 0 {
			if _, werr := child.Write(c.report); werr != nil {
				t.Fatalf("the child could not report: %v", werr)
			}
		}
		//: await closes the parent's copy, but the child's dup must go too or
		//: the read never reaches EOF.
		if cerr := child.Close(); cerr != nil {
			t.Fatalf("closing the child's end: %v", cerr)
		}

		got := hs.await()

		if c.wantCode == 0 {
			if got != nil {
				t.Fatalf("await = %v, want nil for a clean execve", got)
			}
			return
		}
		if !errs.HasCode(got, c.wantCode) {
			t.Fatalf("await = %v, want code %v", got, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handshake_closeBoth pins the abort path: a spawn that fails before await
// must release both descriptors, or every failed spawn leaks a pipe.
func Test_handshake_closeBoth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many times closeBoth is called; the second must not panic.
		calls int
	}
	tests := []tc{
		{"once", 1},
		{"twice", 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hs, err := newHandshake()
		if err != nil {
			t.Fatalf("newHandshake = %v, want nil", err)
		}
		for range c.calls {
			hs.closeBoth()
		}
		//: both ends are gone: a write must now fail rather than land in a
		//: descriptor something else has since been given.
		if _, werr := hs.write.Write([]byte{handshakeApplyFail}); werr == nil {
			t.Error("the write end is still open after closeBoth")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_closeHandshake pins the nil tolerance. A non-trampolined spawn never
// creates a pipe, and the abort path is shared — so a nil check missing here
// would turn every ordinary failed spawn into a panic.
func Test_closeHandshake(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		present bool
	}
	tests := []tc{
		{"a spawn that never created one", false},
		{"a spawn that did", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var hs *handshake
		if c.present {
			created, err := newHandshake()
			if err != nil {
				t.Fatalf("newHandshake = %v, want nil", err)
			}
			hs = created
		}

		closeHandshake(hs)

		if !c.present {
			return
		}
		if _, werr := hs.write.Write([]byte{handshakeApplyFail}); werr == nil {
			t.Error("the write end is still open after closeHandshake")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handshakeError pins the byte→sentinel mapping. Each status names a
// different fix: a limit that could not be applied is a configuration the host
// refuses, a failed execve is a binary problem, and a refused cgroup write is a
// delegation problem. Collapsing them would send an operator to the wrong one.
func Test_handshakeError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		code byte
		want errs.Code
	}
	tests := []tc{
		{"the apply failure", handshakeApplyFail, coreproc.CodeRlimitFailed},
		{"the exec failure", handshakeExecFail, coreproc.CodeSpawnFailed},
		{"the cgroup failure", handshakeCgroupFail, coreproc.CodeCgroupWriteFailed},
		{"an unrecognised byte", '?', coreproc.CodeSpawnFailed},
		{"a zero byte", 0, coreproc.CodeSpawnFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := handshakeError(c.code)
		if got == nil {
			t.Fatalf("handshakeError(%q) = nil, want a typed error", c.code)
		}
		if !errs.HasCode(got, c.want) {
			t.Errorf("handshakeError(%q) = %v, want code %v", c.code, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the three status bytes must be distinct, or the trampoline could not
	//: report which step failed.
	seen := map[byte]struct{}{}
	for _, b := range []byte{handshakeApplyFail, handshakeExecFail, handshakeCgroupFail} {
		if _, dup := seen[b]; dup {
			t.Errorf("two trampoline statuses share the byte %q", b)
		}
		seen[b] = struct{}{}
	}
	//: the fallback descriptor sits after the three std streams.
	if defaultHandshakeFD != 3 {
		t.Errorf("defaultHandshakeFD = %d, want 3", defaultHandshakeFD)
	}
	//: os.Stderr is fd 2, so fd 3 is genuinely the first free slot.
	if os.Stderr.Fd() != 2 {
		t.Errorf("os.Stderr is fd %d, so fd 3 is not the first free slot", os.Stderr.Fd())
	}
}
