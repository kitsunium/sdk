//go:build unix

// Package exec — the re-exec trampoline.
//
// The trampoline exists because Go exposes no hook to run setrlimit(2) or
// umask(2) in the child between fork and exec. Start therefore re-execs THIS
// binary with an encoded payload; the copy applies the limits and execve()s the
// real target. Everything below is either the encoding or the decoding of that
// payload, and a mismatch between the two halves means a child that runs
// unconstrained while Start reports success.
package exec

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Test_installTrampoline pins the pre-main check. It runs in EVERY process that
// links this package, via a package-level var initialiser, so its no-payload
// path has to be free — and, far more importantly, it must not hijack a normal
// program that happens to import the SDK.
func Test_installTrampoline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many times to run the check; it must stay a no-op every time.
		calls int
	}
	tests := []tc{
		{"a single check", 1},
		{"repeated checks", 16},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the test binary itself is a normal process: no payload is set, so
		//: the check must return without exec'ing anything.
		if payload := os.Getenv(trampolineEnv); payload != "" {
			t.Fatalf("the test process carries a trampoline payload %q", payload)
		}
		for range c.calls {
			//: reaching the next statement IS the assertion — the trampoline
			//: path never returns.
			installTrampoline()
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_needsTrampoline pins the predicate that decides whether a spawn pays for
// an extra exec. Taking the trampoline needlessly costs one exec per spawn;
// skipping it when it IS needed silently drops the limits, so both directions
// have to be exact.
func Test_needsTrampoline(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name string
		spec coreproc.Spec
		want bool
	}
	tests := []tc{
		{"a plain spec", coreproc.Spec{Path: "/bin/true"}, false},
		{"an empty rlimit map", coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{}}, false},
		{"an unrelated field", coreproc.Spec{Setpgid: true, Setsid: true}, false},
		{"a umask", coreproc.Spec{Umask: &umask}, true},
		{
			"one rlimit",
			coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile: {Soft: 64, Hard: 64},
			}},
			true,
		},
		//: a cgroup placement also has to happen before execve, so it takes the
		//: same route.
		{"a cgroup placement", coreproc.Spec{CgroupPath: "/sys/fs/cgroup/app"}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := needsTrampoline(c.spec); got != c.want {
			t.Errorf("needsTrampoline(%s) = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_encodeTrampoline pins the payload the child decodes. It carries the
// PLATFORM rlimit number rather than the abstract Resource, because the child
// hands it straight to setrlimit(2) — encoding the enum instead would apply a
// ceiling to whatever resource happened to share that number.
func Test_encodeTrampoline(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name string
		spec coreproc.Spec
		//: substrings the payload must contain.
		wantTokens []string
		wantEmpty  bool
	}
	tests := []tc{
		{name: "nothing to encode", spec: coreproc.Spec{}, wantEmpty: true},
		{
			name:       "a umask alone",
			spec:       coreproc.Spec{Umask: &umask},
			wantTokens: []string{"u" + strconv.Itoa(umask) + ";"},
		},
		{
			name: "one rlimit",
			spec: coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile: {Soft: 64, Hard: 128},
			}},
			wantTokens: []string{
				"r" + strconv.Itoa(resourceLimits[coreproc.ResourceNoFile]) + ",64,128;",
			},
		},
		{
			name: "a umask and an rlimit",
			spec: coreproc.Spec{
				Umask: &umask,
				Rlimits: map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceCore: {Soft: 0, Hard: 0},
				},
			},
			wantTokens: []string{
				"u" + strconv.Itoa(umask) + ";",
				"r" + strconv.Itoa(resourceLimits[coreproc.ResourceCore]) + ",0,0;",
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := encodeTrampoline(c.spec)
		if c.wantEmpty {
			if got != "" {
				t.Fatalf("encodeTrampoline(%s) = %q, want empty", c.name, got)
			}
			return
		}
		for _, want := range c.wantTokens {
			if !strings.Contains(got, want) {
				t.Errorf("the payload %q does not contain %q", got, want)
			}
		}
		//: what encode produces, apply must accept — the two halves are the
		//: only thing standing between a limit and a silently unlimited child.
		if msg := applyTrampoline(got); msg != "" {
			t.Errorf("the encoded payload %q was refused by applyTrampoline: %s", got, msg)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyTrampoline pins the decode loop: every token applies, and the FIRST
// failure aborts. A partially applied payload is the dangerous outcome — a child
// with its umask set but its file-descriptor ceiling missing looks configured
// and is not.
func Test_applyTrampoline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload string
		wantMsg bool
	}
	tests := []tc{
		{name: "an empty payload"},
		{name: "only separators", payload: ";;;"},
		{name: "a single umask", payload: "u18;"},
		{name: "a umask with no trailing separator", payload: "u18"},
		{name: "an unknown tag", payload: "x1;", wantMsg: true},
		{name: "a malformed umask", payload: "unot-a-number;", wantMsg: true},
		{name: "a malformed rlimit", payload: "rnot,a,limit;", wantMsg: true},
		//: the first failure wins, whatever follows it.
		{name: "a good token before a bad one", payload: "u18;x1;", wantMsg: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := applyTrampoline(c.payload)
		if c.wantMsg {
			if got == "" {
				t.Fatalf("applyTrampoline(%q) reported success, want a diagnostic", c.payload)
			}
			return
		}
		if got != "" {
			t.Fatalf("applyTrampoline(%q) = %q, want success", c.payload, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyToken pins the tag dispatch, including the refusal of anything it
// does not recognise. Ignoring an unknown token would be worse than failing:
// the child would exec having applied only the limits it understood.
func Test_applyToken(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		tok     string
		wantMsg bool
	}
	tests := []tc{
		{name: "a umask token", tok: "u18"},
		{name: "an unknown tag", tok: "z1", wantMsg: true},
		{name: "a malformed umask", tok: "uxyz", wantMsg: true},
		{name: "a malformed rlimit", tok: "rxyz", wantMsg: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := applyToken(c.tok)
		if c.wantMsg != (got != "") {
			t.Errorf("applyToken(%q) = %q, want a diagnostic: %v", c.tok, got, c.wantMsg)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyUmaskToken pins the parse and the syscall. The umask is process-wide
// and the trampoline is about to exec, so applying it in this test process means
// restoring it afterwards — which is exactly what the syscall's own return value
// is for.
func Test_applyUmaskToken(t *testing.T) {
	//: umask(2) is process-wide, so every case below takes umaskMu for the
	//: whole set-observe-restore sequence. That is what lets the sub-cases run
	//: in parallel without observing each other's mask.
	t.Parallel()
	type tc struct {
		name    string
		in      string
		wantMsg bool
	}
	tests := []tc{
		{name: "a decimal mask", in: "18"},
		{name: "zero", in: "0"},
		{name: "a non-integer", in: "not-a-number", wantMsg: true},
		{name: "an empty value", in: "", wantMsg: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		umaskMu.Lock()
		defer umaskMu.Unlock()
		//: capture the current mask so the process is left as it was found.
		before := syscall.Umask(0)
		syscall.Umask(before)
		defer syscall.Umask(before)

		got := applyUmaskToken(c.in)
		if c.wantMsg {
			if got == "" {
				t.Fatalf("applyUmaskToken(%q) reported success, want a diagnostic", c.in)
			}
			//: a refused token must leave the mask alone.
			if now := syscall.Umask(before); now != before {
				t.Errorf("a malformed token changed the umask to %#o", now)
			}
			return
		}
		if got != "" {
			t.Fatalf("applyUmaskToken(%q) = %q, want success", c.in, got)
		}
		want, cerr := strconv.Atoi(c.in)
		if cerr != nil {
			t.Fatalf("the fixture %q is not an integer: %v", c.in, cerr)
		}
		if now := syscall.Umask(before); now != want {
			t.Errorf("the umask is %#o, want %#o", now, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// umaskMu serialises the process-wide umask across the parallel sub-cases that
// set and observe it.
var umaskMu sync.Mutex

// Test_applyRlimitToken pins the three-field parse and the setrlimit failure
// message.
//
// The message names the soft/hard pair on purpose: by the time the parent reads
// it the child is gone, and a per-kernel rejection ("this host will not let you
// raise that") is otherwise indistinguishable from a malformed payload.
func Test_applyRlimitToken(t *testing.T) {
	//: not parallel — setrlimit(2) changes a process-wide ceiling.
	//
	//: RLIMIT_CORE rather than RLIMIT_NOFILE on purpose. The Go runtime raises
	//: the soft NOFILE ceiling to the hard one at startup while Getrlimit still
	//: reports the ORIGINAL soft value, so "read it and write it back" silently
	//: lowers the process from ~1M descriptors to ~1k — and every parallel test
	//: opening a pipe then fails. Core dumps have no such asymmetry.
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &before); err != nil {
		panic("getrlimit(CORE): " + err.Error())
	}

	type tc struct {
		name    string
		in      string
		wantMsg bool
	}
	core := strconv.Itoa(syscall.RLIMIT_CORE)
	tests := []tc{
		{
			name: "the current ceiling re-applied",
			in:   core + "," + strconv.FormatUint(before.Cur, 10) + "," + strconv.FormatUint(before.Max, 10),
		},
		{name: "core dumps disabled", in: core + ",0,0"},
		{name: "no separators at all", in: "8", wantMsg: true},
		{name: "only one separator", in: "8,64", wantMsg: true},
		{name: "a non-numeric resource", in: "x,64,64", wantMsg: true},
		{name: "a non-numeric soft ceiling", in: core + ",x,64", wantMsg: true},
		{name: "a non-numeric hard ceiling", in: core + ",64,x", wantMsg: true},
		//: a soft ceiling above the hard one is refused by the kernel, and the
		//: message must name the pair so the rejection is diagnosable.
		{name: "a soft ceiling above the hard one", in: core + ",99999999,0", wantMsg: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Cleanup(func() {
			//: restore the process-wide ceiling whatever the case did.
			restore := before
			if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &restore); err != nil {
				t.Logf("restoring RLIMIT_CORE: %v", err)
			}
		})

		got := applyRlimitToken(c.in)
		if c.wantMsg {
			if got == "" {
				t.Fatalf("applyRlimitToken(%q) reported success, want a diagnostic", c.in)
			}
			return
		}
		if got != "" {
			t.Fatalf("applyRlimitToken(%q) = %q, want success", c.in, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_environWithout pins the sentinel stripping. The payload variable MUST NOT
// reach the real target: it links the same SDK, so its own pre-main check would
// fire and it would trampoline itself — recursively.
func Test_environWithout(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		env  []string
		key  string
		want []string
	}
	tests := []tc{
		{"an empty environment", nil, trampolineEnv, []string{}},
		{"nothing to remove", []string{"PATH=/bin"}, trampolineEnv, []string{"PATH=/bin"}},
		{
			"the sentinel removed",
			[]string{"PATH=/bin", trampolineEnv + "=u18;"},
			trampolineEnv,
			[]string{"PATH=/bin"},
		},
		{
			//: a duplicated sentinel must go entirely, not merely once.
			"a duplicated key",
			[]string{trampolineEnv + "=a", "PATH=/bin", trampolineEnv + "=b"},
			trampolineEnv,
			[]string{"PATH=/bin"},
		},
		{
			//: a value that merely mentions the key is not the key.
			"a value mentioning the key",
			[]string{"NOTES=" + trampolineEnv + " is set"},
			trampolineEnv,
			[]string{"NOTES=" + trampolineEnv + " is set"},
		},
		{
			"an entry with no value",
			[]string{trampolineEnv},
			trampolineEnv,
			[]string{},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := environWithout(c.env, c.key)
		if !slices.Equal(got, c.want) {
			t.Errorf("environWithout(%v, %q) = %v, want %v", c.env, c.key, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_childHandshakeFD pins the descriptor resolution, including the fallback.
// An unparseable value must NOT be trusted: writing a status byte into an
// arbitrary descriptor would corrupt whatever the child had open there.
func Test_childHandshakeFD(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name string
		env  string
		want int
	}
	tests := []tc{
		{"no variable set", "", defaultHandshakeFD},
		{"an explicit descriptor", "7", 7},
		{"the default written explicitly", "3", 3},
		{"a non-numeric value falls back", "not-a-number", defaultHandshakeFD},
		{"an empty value falls back", "", defaultHandshakeFD},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(trampolineHsFdEnv, c.env)
		if got := childHandshakeFD(); got != c.want {
			t.Errorf("childHandshakeFD() with %q = %d, want %d", c.env, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_reportHandshake pins that reporting is best-effort. It runs microseconds
// before the child exits, so a write failure changes nothing — but a PANIC there
// would replace a clean typed error with a crash the parent cannot interpret.
func Test_reportHandshake(t *testing.T) {
	//: not parallel — it points the handshake fd at a test pipe via the env.
	type tc struct {
		name string
		code byte
		//: whether the descriptor the env names is actually open.
		open bool
	}
	tests := []tc{
		{"an apply failure to an open pipe", handshakeApplyFail, true},
		{"an exec failure to an open pipe", handshakeExecFail, true},
		{"a cgroup failure to an open pipe", handshakeCgroupFail, true},
		{"a report into a closed descriptor", handshakeApplyFail, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.open {
			//: a descriptor no process has open; the write fails and must be
			//: swallowed rather than panicking.
			t.Setenv(trampolineHsFdEnv, "9999")
			reportHandshake(c.code)
			return
		}

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		defer func() {
			if cerr := r.Close(); cerr != nil {
				t.Logf("closing the read end: %v", cerr)
			}
			if cerr := w.Close(); cerr != nil {
				t.Logf("closing the write end: %v", cerr)
			}
		}()
		t.Setenv(trampolineHsFdEnv, strconv.Itoa(int(w.Fd())))

		reportHandshake(c.code)

		buf := make([]byte, 1)
		n, rerr := r.Read(buf)
		if rerr != nil || n != 1 {
			t.Fatalf("reading the report: n=%d err=%v", n, rerr)
		}
		if buf[0] != c.code {
			t.Errorf("the report is %q, want %q", buf[0], c.code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_armHandshakeClose pins the close-on-exec arming, which is what makes EOF
// mean success on the parent side. Without it a successful execve would leave the
// pipe open and the parent would block reading a descriptor nobody will ever
// write to.
func Test_armHandshakeClose(t *testing.T) {
	//: not parallel — it points the handshake fd at a test pipe via the env.
	type tc struct {
		name string
	}
	tests := []tc{{"an open pipe descriptor"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		defer func() {
			if cerr := r.Close(); cerr != nil {
				t.Logf("closing the read end: %v", cerr)
			}
			if cerr := w.Close(); cerr != nil {
				t.Logf("closing the write end: %v", cerr)
			}
		}()
		t.Setenv(trampolineHsFdEnv, strconv.Itoa(int(w.Fd())))

		armHandshakeClose()

		//: FD_CLOEXEC must now be set, or execve would not close it.
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, w.Fd(), syscall.F_GETFD, 0)
		if errno != 0 {
			t.Fatalf("reading the descriptor flags: %v", errno)
		}
		if flags&syscall.FD_CLOEXEC == 0 {
			t.Error("FD_CLOEXEC is not set, so a successful execve would leave the pipe open")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_stderrLine pins that the diagnostic writer never fails the caller. It is
// the trampoline's only voice — the parent gets one status byte, and everything
// else a human needs is this line — but it runs on the way out, so a write fault
// must not become a second failure.
func Test_stderrLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		msg  string
	}
	tests := []tc{
		{"an ordinary message", "sdk trampoline exec /bin/true: no such file"},
		{"an empty message", ""},
		{"a message with a newline already", "line one\nline two"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: reaching the next statement is the assertion: this must never panic
		//: or block, whatever stderr happens to be.
		stderrLine(c.msg)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trampolineSpawn pins the argv layout the child depends on: argv[0] is
// this binary, argv[1] is the target to exec, and argv[2:] is the target's own
// argv. Getting the offset wrong would exec the target with its own path as its
// first argument, which most programs treat as a filename.
func Test_trampolineSpawn(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name     string
		spec     coreproc.Spec
		wantArgv []string
		wantCgrp bool
	}
	tests := []tc{
		{
			name:     "a target with no arguments",
			spec:     coreproc.Spec{Path: "/bin/true", Umask: &umask},
			wantArgv: []string{"/bin/true", "/bin/true"},
		},
		{
			name:     "a target with its own argv",
			spec:     coreproc.Spec{Path: "/bin/sh", Args: []string{"sh", "-c", "true"}, Umask: &umask},
			wantArgv: []string{"/bin/sh", "sh", "-c", "true"},
		},
		{
			name:     "a cgroup placement rides in its own variable",
			spec:     coreproc.Spec{Path: "/bin/true", CgroupPath: "/sys/fs/cgroup/app"},
			wantArgv: []string{"/bin/true", "/bin/true"},
			wantCgrp: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("resolving the test binary: %v", err)
		}

		path, argv, addon, terr := trampolineSpawn(c.spec)
		if terr != nil {
			t.Fatalf("trampolineSpawn(%s) = %v, want nil", c.name, terr)
		}
		if path != self {
			t.Errorf("path = %q, want this binary %q", path, self)
		}
		want := append([]string{self}, c.wantArgv...)
		if !slices.Equal(argv, want) {
			t.Errorf("argv = %v, want %v", argv, want)
		}
		//: the payload variable is always present; the cgroup one only when a
		//: placement was asked for, because a path can carry any byte and must
		//: not need escaping inside the ';'-delimited payload.
		var hasPayload, hasCgroup bool
		for _, kv := range addon {
			switch {
			case strings.HasPrefix(kv, trampolineEnv+"="):
				hasPayload = true
			case strings.HasPrefix(kv, trampolineCgroupEnv+"="):
				hasCgroup = true
			}
		}
		if !hasPayload {
			t.Errorf("the addon %v carries no payload variable", addon)
		}
		if hasCgroup != c.wantCgrp {
			t.Errorf("a cgroup variable is present = %v, want %v", hasCgroup, c.wantCgrp)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
