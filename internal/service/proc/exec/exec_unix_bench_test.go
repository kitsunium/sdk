//go:build unix

// Package exec — the measurement that decides whether anything in this package
// is worth optimising: how much of a real process launch is SDK preparation and
// how much is the kernel's fork/exec.
//
// It is an INTERNAL benchmark on purpose. Everything Start does before
// os.StartProcess is unexported (validateSpec, checkLimits, buildStdio,
// resolveSpawn, buildProcAttr…), and those are exactly the pure-CPU functions a
// caller could in principle be charged for on every launch. Measuring only the
// exported Start would report one number that answers nothing.
package exec

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// benchTrue is the cheapest possible child: a binary whose entire job is to exit
// 0. Using it makes Start's end-to-end number as close to the pure fork/exec
// floor as a real launch can get, so the preparation share is not flattered by a
// child that does work of its own.
const benchTrue string = "/bin/true"

// benchSleep is a child that stays alive, so the handle operations (Signal,
// SignalGroup) measure a delivery to a live process rather than an ESRCH.
const benchSleep string = "/bin/sleep"

// Sinks defeat dead-code elimination: a prepared ProcAttr that is never observed
// can be proven unused, and the compiler would then measure nothing.
var (
	errSink   error
	boolSink  bool
	strSink   string
	intSink   int
	argvSink  []string
	envSink   []string
	attrSink  *os.ProcAttr
	stdioSink *stdioState
	credSink  *syscall.Credential
	filesSink []*os.File
	exitSink  coreproc.ExitValue
	procSink  coreproc.Process
)

// requireBinary skips the benchmark when the host has no such executable, rather
// than reporting a number produced by a failing spawn.
func requireBinary(b *testing.B, path string) {
	b.Helper()
	if _, err := os.Stat(path); err != nil {
		b.Skipf("%s is not available on this host: %v", path, err)
	}
}

// prepareOnly runs exactly the work Start performs BEFORE os.StartProcess, then
// releases everything it opened. It mirrors Start's prologue call for call —
// keep the two in step if Start's ordering changes.
//
// The releases are inside the measured region for the modes that open fds
// (Null/Capture/trampoline); that is stated in BENCH.md rather than hidden,
// because a benchmark that leaks a pipe per iteration exhausts the fd table
// long before it produces a stable number.
func prepareOnly(spec coreproc.Spec) (*os.ProcAttr, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	if err := checkLimits(spec); err != nil {
		return nil, err
	}
	if err := validateCgroupPath(spec.CgroupPath); err != nil {
		return nil, err
	}
	sio, err := buildStdio(spec)
	if err != nil {
		return nil, err
	}
	path, argv, envAddon, err := resolveSpawn(spec)
	if err != nil {
		sio.closeAll()
		return nil, err
	}
	files, hs, err := spawnFiles(spec, sio)
	if err != nil {
		sio.closeAll()
		return nil, err
	}
	childFiles, hsAddon := childFileTable(files, spec.ExtraFiles, hs)
	attr, err := buildProcAttr(spec, childFiles, append(envAddon, hsAddon...))
	closeHandshake(hs)
	sio.closeAll()
	strSink, argvSink = path, argv
	return attr, err
}

// ── The headline: preparation against a whole launch ─────────────────────────

// BenchmarkPrepare_Direct_Inherit is the cheapest preparation the package can
// do: no credentials, no limits, no cgroup, and stdio shared with the parent, so
// not one file descriptor is created. It is the number to compare against
// BenchmarkStart_BinTrue_Inherit.
func BenchmarkPrepare_Direct_Inherit(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Args: []string{benchTrue}, Setpgid: true}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		attr, err := prepareOnly(spec)
		attrSink, errSink = attr, err
	}
}

// BenchmarkPrepare_Direct_Env32 is the same preparation carrying a realistic
// environment. buildProcAttr copies Spec.Env into a fresh slice on every Start —
// deliberately, so a nil never leaks the supervisor's environment — and this row
// prices that copy.
func BenchmarkPrepare_Direct_Env32(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue, Args: []string{benchTrue},
		Env: benchEnv(32), Setpgid: true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		attr, err := prepareOnly(spec)
		attrSink, errSink = attr, err
	}
}

// BenchmarkPrepare_Direct_Capture prices the stdio mode that actually creates
// descriptors: two pipes for stdout/stderr plus a null device for stdin.
func BenchmarkPrepare_Direct_Capture(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue, Args: []string{benchTrue},
		Stdio: coreproc.StdioCapture, Stdout: os.Stderr, Stderr: os.Stderr,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		attr, err := prepareOnly(spec)
		attrSink, errSink = attr, err
	}
}

// BenchmarkPrepare_Trampoline is the preparation for a Spec that sets an rlimit:
// it additionally resolves os.Executable(), encodes the limits payload, and
// creates the handshake pipe. It is the price of asking for a pre-exec limit
// BEFORE the second execve that asking for one implies.
func BenchmarkPrepare_Trampoline(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue, Args: []string{benchTrue},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 1024, Hard: 4096},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		attr, err := prepareOnly(spec)
		attrSink, errSink = attr, err
	}
}

// BenchmarkStart_BinTrue_Inherit is the floor of a real launch: fork, execve,
// wait4. Everything the SDK does is a fraction of this.
func BenchmarkStart_BinTrue_Inherit(b *testing.B) {
	requireBinary(b, benchTrue)
	spec := coreproc.Spec{Path: benchTrue, Args: []string{benchTrue}, Setpgid: true}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := Start(b.Context(), spec)
		if err != nil {
			b.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}

// BenchmarkStart_BinTrue_Null adds the three null-device opens to the same
// launch, so the delta against Inherit is what StdioNull costs.
func BenchmarkStart_BinTrue_Null(b *testing.B) {
	requireBinary(b, benchTrue)
	spec := coreproc.Spec{
		Path: benchTrue, Args: []string{benchTrue},
		Stdio: coreproc.StdioNull, Setpgid: true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := Start(b.Context(), spec)
		if err != nil {
			b.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}

// BenchmarkStart_BinTrue_Trampoline is the same launch routed through the
// re-exec trampoline because the Spec asks for one rlimit. The delta against
// Inherit is the price of the SECOND execve the Go runtime forces on anyone who
// needs setrlimit(2) between fork and exec — the single most expensive decision
// a caller of this package can make, and it is invisible in the API.
func BenchmarkStart_BinTrue_Trampoline(b *testing.B) {
	requireBinary(b, benchTrue)
	spec := coreproc.Spec{
		Path: benchTrue, Args: []string{benchTrue}, Setpgid: true,
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 1024, Hard: 4096},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := Start(b.Context(), spec)
		if err != nil {
			b.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}

// ── Component attribution ────────────────────────────────────────────────────

// BenchmarkStart_SelfBinary_NoOp exists to EXPLAIN the gap between
// Start_BinTrue_Inherit and Start_BinTrue_Trampoline instead of leaving it to
// intuition. A trampolined spawn execs THIS binary first and only then execve()s
// the target, so the hypothesis under test is that the gap is the cost of
// loading and initialising the SDK's own binary — not anything the trampoline
// code does. This row spawns the benchmark binary with flags that make it run
// nothing, which is the closest measurable stand-in for that first exec.
func BenchmarkStart_SelfBinary_NoOp(b *testing.B) {
	self, err := os.Executable()
	if err != nil {
		b.Skipf("os.Executable: %v", err)
	}
	spec := coreproc.Spec{
		Path: self,
		// No test and no benchmark matches, so the child initialises and exits.
		Args:  []string{self, "-test.run=^$", "-test.bench=^$"},
		Stdio: coreproc.StdioNull, Setpgid: true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, sErr := Start(b.Context(), spec)
		if sErr != nil {
			b.Fatalf("Start: %v", sErr)
		}
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}

func BenchmarkValidateSpec(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = validateSpec(spec)
	}
}

func BenchmarkCheckLimits_None(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = checkLimits(spec)
	}
}

func BenchmarkCheckLimits_Three(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue,
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 1024, Hard: 4096},
			coreproc.ResourceCore:   {Soft: 0, Hard: 0},
			coreproc.ResourceCPU:    {Soft: 60, Hard: 120},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = checkLimits(spec)
	}
}

func BenchmarkValidateCgroupPath_Empty(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = validateCgroupPath("")
	}
}

func BenchmarkNeedsTrampoline_No(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = needsTrampoline(spec)
	}
}

func BenchmarkBuildArgv_Default(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		argvSink = buildArgv(spec)
	}
}

func BenchmarkBuildArgv_Explicit(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Args: []string{benchTrue, "-x", "-y"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		argvSink = buildArgv(spec)
	}
}

// BenchmarkBuildStdio_Inherit allocates one stdioState and copies three
// *os.File pointers. Nothing is opened, so nothing is closed.
func BenchmarkBuildStdio_Inherit(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := buildStdio(spec)
		stdioSink, errSink = s, err
	}
}

// BenchmarkBuildStdio_Null measures build + closeAll: three opens of the null
// device and three closes. The pair is the honest unit — a loop that only opened
// would exhaust the descriptor table.
func BenchmarkBuildStdio_Null(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Stdio: coreproc.StdioNull}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := buildStdio(spec)
		if err == nil {
			s.closeAll()
		}
		stdioSink, errSink = s, err
	}
}

// BenchmarkBuildStdio_Capture measures build + closeAll for two pipes (four
// descriptors) plus one null device for stdin.
func BenchmarkBuildStdio_Capture(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue, Stdio: coreproc.StdioCapture,
		Stdout: os.Stderr, Stderr: os.Stderr,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := buildStdio(spec)
		if err == nil {
			s.closeAll()
		}
		stdioSink, errSink = s, err
	}
}

// BenchmarkResolveCredential_None is the path every Spec that does not change
// identity takes: three string/len comparisons and a nil return.
func BenchmarkResolveCredential_None(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c, err := resolveCredential(spec)
		credSink, errSink = c, err
	}
}

// BenchmarkResolveCredential_NumericUID takes the documented shortcut: a numeric
// token is parsed rather than looked up. os/user is still consulted for the
// primary gid, so this is NOT allocation-free.
func BenchmarkResolveCredential_NumericUID(b *testing.B) {
	self, err := user.Current()
	if err != nil {
		b.Skipf("os/user cannot resolve the current user: %v", err)
	}
	spec := coreproc.Spec{Path: benchTrue, User: self.Uid}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c, cErr := resolveCredential(spec)
		credSink, errSink = c, cErr
	}
}

// BenchmarkResolveCredential_NamedUser is the NSS round trip: a name that is not
// numeric goes through user.Lookup. This is the one preparation step that can
// plausibly dwarf everything else in the package, so it is measured on its own.
func BenchmarkResolveCredential_NamedUser(b *testing.B) {
	self, err := user.Current()
	if err != nil {
		b.Skipf("os/user cannot resolve the current user: %v", err)
	}
	spec := coreproc.Spec{Path: benchTrue, User: self.Username}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c, cErr := resolveCredential(spec)
		credSink, errSink = c, cErr
	}
}

// BenchmarkResolveCredential_NumericGroups prices the supplementary-group loop
// on the numeric path, where no NSS lookup happens at all.
func BenchmarkResolveCredential_NumericGroups(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Groups: []string{"10", "20", "30", "40"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c, err := resolveCredential(spec)
		credSink, errSink = c, err
	}
}

func BenchmarkBuildProcAttr_Env0(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue}
	files := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		a, err := buildProcAttr(spec, files, nil)
		attrSink, errSink = a, err
	}
}

func BenchmarkBuildProcAttr_Env32(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Env: benchEnv(32)}
	files := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		a, err := buildProcAttr(spec, files, nil)
		attrSink, errSink = a, err
	}
}

func BenchmarkAppendExtraFiles_4(b *testing.B) {
	std := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	extra := []*os.File{os.Stdin, os.Stdin, os.Stdin, os.Stdin}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		filesSink = appendExtraFiles(std, extra)
	}
}

func BenchmarkEncodeTrampoline_Umask(b *testing.B) {
	spec := coreproc.Spec{Path: benchTrue, Umask: new(0o027)}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = encodeTrampoline(spec)
	}
}

func BenchmarkEncodeTrampoline_ThreeRlimits(b *testing.B) {
	spec := coreproc.Spec{
		Path: benchTrue,
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 1024, Hard: 4096},
			coreproc.ResourceCore:   {Soft: 0, Hard: 0},
			coreproc.ResourceCPU:    {Soft: 60, Hard: 120},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = encodeTrampoline(spec)
	}
}

// BenchmarkOSExecutable is not SDK code — it is the readlink("/proc/self/exe")
// that trampolineSpawn calls on every trampolined Start. It is measured because
// it is the only syscall in the trampoline's preparation and a reader comparing
// Prepare_Trampoline against Prepare_Direct needs to know where the gap went.
func BenchmarkOSExecutable(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := os.Executable()
		strSink, errSink = s, err
	}
}

// BenchmarkEnvironWithout_64 is the trampoline CHILD's environment strip, run
// three times per trampolined launch (once per sentinel variable) between the
// setrlimit and the execve. It is the only unbounded loop on that path.
func BenchmarkEnvironWithout_64(b *testing.B) {
	env := benchEnv(64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		envSink = environWithout(env, trampolineEnv)
	}
}

// BenchmarkApplyTrampoline_ThreeRlimits is the trampoline child's decode+apply.
// It issues three real setrlimit(2) calls, each re-setting the resource to the
// value it already holds, so the process is left exactly as it was found.
func BenchmarkApplyTrampoline_ThreeRlimits(b *testing.B) {
	payload := benchLimitPayload(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = applyTrampoline(payload)
	}
	if strSink != "" {
		b.Fatalf("applyTrampoline: %s", strSink)
	}
}

// ── Handle operations ────────────────────────────────────────────────────────

// BenchmarkHandle_PID is the port's cheapest method. It exists to prove the
// accessor is a field read and not a syscall.
func BenchmarkHandle_PID(b *testing.B) {
	h := &handle{pid: 4242, pgid: 4242, setpgid: true, stdio: &stdioState{}, done: make(chan struct{})}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = h.PID()
	}
}

// BenchmarkHandle_Wait_Memoised prices the second and every later Wait: the
// sync.Once fast path over an already-reaped child. A supervisor that calls Wait
// from several goroutines pays this, not a wait4.
func BenchmarkHandle_Wait_Memoised(b *testing.B) {
	requireBinary(b, benchTrue)
	p, err := Start(b.Context(), coreproc.Spec{Path: benchTrue, Args: []string{benchTrue}})
	if err != nil {
		b.Fatalf("Start: %v", err)
	}
	if _, wErr := p.Wait(); wErr != nil {
		b.Fatalf("first Wait: %v", wErr)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}

// BenchmarkHandle_Signal_Live delivers SIGCONT to a live child on every
// iteration: one kill(2) plus the SDK's error-free return path. SIGCONT is used
// because it is harmless to an already-running process.
func BenchmarkHandle_Signal_Live(b *testing.B) {
	p := startSleeper(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = p.Signal(coreproc.Signal(syscall.SIGCONT))
	}
	b.StopTimer()
	stopSleeper(b, p)
}

// BenchmarkHandle_SignalGroup_Live is the same delivery addressed at -pgid, the
// call Stop is built on.
func BenchmarkHandle_SignalGroup_Live(b *testing.B) {
	p := startSleeper(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = p.SignalGroup(coreproc.Signal(syscall.SIGCONT))
	}
	b.StopTimer()
	stopSleeper(b, p)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// startSleeper spawns a long-lived child in its own process group so the signal
// benchmarks address a real, live target and never the test runner's own group.
func startSleeper(b *testing.B) coreproc.Process {
	b.Helper()
	requireBinary(b, benchSleep)
	p, err := Start(b.Context(), coreproc.Spec{
		Path: benchSleep, Args: []string{benchSleep, "3600"},
		Stdio: coreproc.StdioNull, Setpgid: true,
	})
	if err != nil {
		b.Skipf("cannot spawn %s: %v", benchSleep, err)
	}
	procSink = p
	return p
}

// stopSleeper kills and reaps the long-lived child so the benchmark binary
// leaves no process behind.
func stopSleeper(b *testing.B, p coreproc.Process) {
	b.Helper()
	if err := p.SignalGroup(coreproc.Signal(syscall.SIGKILL)); err != nil {
		b.Logf("SignalGroup(SIGKILL): %v", err)
	}
	if _, err := p.Wait(); err != nil {
		b.Logf("Wait: %v", err)
	}
}

// benchEnv builds an n-entry environment of realistic shape (KEY=value, no
// duplicates), built once outside every timed loop.
func benchEnv(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "KITSU_BENCH_VAR_"+strconv.Itoa(i)+"=value-"+strconv.Itoa(i))
	}
	return out
}

// benchLimitPayload encodes three rlimit tokens carrying each resource's CURRENT
// soft/hard pair, so applying the payload is a real setrlimit(2) that changes
// nothing observable about the benchmark process.
func benchLimitPayload(b *testing.B) string {
	b.Helper()
	payload := ""
	for _, res := range []int{syscall.RLIMIT_NOFILE, syscall.RLIMIT_CORE, syscall.RLIMIT_CPU} {
		var rl syscall.Rlimit
		if err := syscall.Getrlimit(res, &rl); err != nil {
			b.Skipf("getrlimit(%d): %v", res, err)
		}
		// Rendered with %d rather than strconv: syscall.Rlimit's fields are int64
		// on FreeBSD/DragonFly and uint64 elsewhere, so any explicit conversion is
		// redundant on one half of the //go:build unix targets and required on the
		// other. This is setup, outside every timed loop.
		payload += fmt.Sprintf("r%d,%d,%d;", res, rl.Cur, rl.Max)
	}
	return payload
}
