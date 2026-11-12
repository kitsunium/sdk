//go:build linux

package service_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The guard on this package's helpers. A test binary started with helperEnv
// plays a service or a supervisor instead of the tests. On 2026-11-08 a
// supervisor's child read the supervisor's role out of a duplicated variable
// and supervised in turn, Restart=always relaunching every level every 10 ms,
// until the machine died of it. So every run of a helper passes this guard
// before it does anything, and the guard depends on no spawn site: one that
// forgets helperEnviron hands the depth on all the same, through os.Environ,
// because the guard writes it into its own environment.

const (
	// depthEnv is how many helpers deep this process is, itself included.
	depthEnv = "PROC_SERVICE_TEST_DEPTH"
	// lifetimeEnv shortens helperLifetime, for the guard's own test.
	lifetimeEnv = "PROC_SERVICE_TEST_HELPER_LIFETIME"
	// witnessEnv names the file a guard probe writes when it runs.
	witnessEnv = "PROC_SERVICE_TEST_WITNESS"
	// guardRefusal is the exit status of a helper the guard refused.
	guardRefusal = 97
	// maxHelperDepth is a supervisor and its service.
	maxHelperDepth = 2
	// supervisorDepth is the one depth a supervisor is admitted at: a child
	// of the test, never a child of a helper.
	supervisorDepth = 1
	// maxHelperRuns bounds the helpers one store directory ever sees.
	maxHelperRuns = 64
	// helperRunsFile holds one byte per helper run under a store directory.
	helperRunsFile = "helper-runs"
	// helperLifetime bounds a helper's life, whatever became of the test.
	helperLifetime = 120 * time.Second
	// restartBurst and restartWindow bound a real Restart=always unit of
	// these tests: its starts number three at most (the first, a crash before
	// a hand-over, a crash after one) and the limiter's count crosses the
	// hand-over, while the window outlasts any test.
	restartBurst  = 8
	restartWindow = 5 * time.Minute
	// holdersBudget bounds killHolders; holdersPoll separates its sweeps,
	// and holdersQuiet is how many in a row must find nothing: a unit's
	// restart may be on its way when the sweep before it found none.
	holdersBudget = 5 * time.Second
	holdersPoll   = 20 * time.Millisecond
	holdersQuiet  = 3
	// probeWait bounds the wait for a guard probe.
	probeWait = 30 * time.Second
	// bazelTimeoutEnv is Bazel's test timeout. rules_go's generated main reads
	// it and then registers a SIGTERM channel nobody reads
	// (bzltestutil.RegisterTimeoutHandler), so a helper that inherited it
	// swallowed every SIGTERM until it registered its own: a stop that landed
	// between a helper's READY=1 and its signal.Notify waited for the final
	// kill (run 43144125124). A helper is a service, not a test: it never
	// inherits the variable.
	bazelTimeoutEnv = "TEST_TIMEOUT"
)

// supervisorHelpers are the helpers that spawn further helpers.
var supervisorHelpers = map[string]bool{
	"handover-old":     true,
	"handover-new":     true,
	"guard-supervisor": true,
}

// admittedDepth is this helper's depth, set by the guard.
var admittedDepth int

func init() {
	helpers["guard-probe"] = witnessProbe
	helpers["guard-supervisor"] = witnessProbe
	helpers["guard-hang"] = func() { time.Sleep(time.Hour) }
}

// witnessProbe writes the witness and ends.
func witnessProbe() {
	path := os.Getenv(witnessEnv)
	if !filepath.IsAbs(path) || os.WriteFile(path, []byte("ran"), 0o600) != nil {
		os.Exit(2)
	}
}

// entryName is an environment entry's name.
func entryName(kv string) string {
	name, _, _ := strings.Cut(kv, "=")
	return name
}

// helperEnviron is this process's environment for a child that plays helper,
// with extra's assignments: helperEnv and each name of extra are taken out
// before they are set, so none reaches the child twice, and Bazel's test
// timeout is taken out for good (bazelTimeoutEnv). The depth stays, as the
// guard left it.
func helperEnviron(helper string, extra ...string) []string {
	drop := map[string]bool{helperEnv: true, bazelTimeoutEnv: true}
	for _, kv := range extra {
		drop[entryName(kv)] = true
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return drop[entryName(kv)] })
	env = append(env, helperEnv+"="+helper)
	return append(env, extra...)
}

// reexecEnviron is the environment of a supervisor re-executing itself as
// helper: the depth as the guard read it, before its increment, so the
// incoming image is the test's child again and not its own.
func reexecEnviron(helper string) []string {
	return helperEnviron(helper, depthEnv+"="+strconv.Itoa(admittedDepth-1))
}

// refuseHelper ends this process without running its helper.
func refuseHelper(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "helper guard: "+format+"\n", args...)
	os.Exit(guardRefusal)
}

// refuseLostRole refuses a process that holds a depth and no role: a helper's
// child that lost helperEnv, which would otherwise run the tests in turn and
// start every helper again.
func refuseLostRole() {
	if raw, set := os.LookupEnv(depthEnv); set {
		refuseHelper("%s=%s without %s", depthEnv, raw, helperEnv)
	}
}

// admitHelper is the guard: it returns the helper named, or ends the process
// with guardRefusal. Anything it cannot establish is a refusal.
func admitHelper(name string) func() {
	helper, known := helpers[name]
	if !known {
		refuseHelper("unknown helper %q", name)
	}
	checkOneRole()
	admittedDepth = enterDepth(name)
	countRun()
	armLifetime()
	return helper
}

// checkOneRole refuses more than one helperEnv in the environment as the
// kernel handed it: os.Environ has already dropped every duplicate but the
// first, the one a child reads.
func checkOneRole() {
	raw, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		refuseHelper("reading /proc/self/environ: %v", err)
	}
	count := 0
	for entry := range bytes.SplitSeq(raw, []byte{0}) {
		if bytes.HasPrefix(entry, []byte(helperEnv+"=")) {
			count++
		}
	}
	if count != 1 {
		refuseHelper("%d occurrences of %s in the raw environment", count, helperEnv)
	}
}

// enterDepth reads the depth, adds this helper to it and writes it back into
// the environment every child of this process will inherit.
func enterDepth(name string) int {
	depth := 0
	if raw, set := os.LookupEnv(depthEnv); set {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			refuseHelper("unreadable %s %q", depthEnv, raw)
		}
		depth = n
	}
	depth++
	if depth > maxHelperDepth {
		refuseHelper("depth %d is past %d", depth, maxHelperDepth)
	}
	if supervisorHelpers[name] && depth != supervisorDepth {
		refuseHelper("supervisor %q at depth %d: only a child of the test may supervise", name, depth)
	}
	if err := os.Setenv(depthEnv, strconv.Itoa(depth)); err != nil {
		refuseHelper("setting %s: %v", depthEnv, err)
	}
	return depth
}

// countRun counts this run under its store directory, when it has one: one
// byte appended, the file's size the number of runs so far, whichever process
// wrote the others.
func countRun() {
	dir, set := os.LookupEnv(storeDirEnv)
	if !set {
		return
	}
	if !filepath.IsAbs(dir) {
		refuseHelper("%s %q is not absolute", storeDirEnv, dir)
	}
	f, err := os.OpenFile(filepath.Join(dir, helperRunsFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		refuseHelper("counting this run: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write([]byte{'.'}); err != nil {
		refuseHelper("counting this run: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		refuseHelper("counting this run: %v", err)
	}
	if info.Size() > maxHelperRuns {
		refuseHelper("run %d under %s is past %d", info.Size(), dir, maxHelperRuns)
	}
}

// armLifetime kills this process at the end of its lifetime, a variable that
// may only shorten it. A supervisor's re-exec starts another.
func armLifetime() {
	lifetime := helperLifetime
	if raw, set := os.LookupEnv(lifetimeEnv); set {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > helperLifetime {
			refuseHelper("unusable %s %q", lifetimeEnv, raw)
		}
		lifetime = d
	}
	time.AfterFunc(lifetime, func() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL) })
}

// holdsMark reports whether pid's environment holds the entry mark exactly.
func holdsMark(pid int, mark []byte) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	for entry := range bytes.SplitSeq(raw, []byte{0}) {
		if bytes.Equal(entry, mark) {
			return true
		}
	}
	return false
}

// killHeld kills pid through a handle, once the handle is proved to hold the
// process that carries mark: the environment read again after it was opened.
func killHeld(pid int, mark []byte) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = p.Release() }()
	return holdsMark(pid, mark) && p.Kill() == nil
}

// killMarked sweeps /proc once and returns how many processes it killed.
func killMarked(mark []byte) int {
	entries, _ := os.ReadDir("/proc")
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err == nil && pid != os.Getpid() && holdsMark(pid, mark) && killHeld(pid, mark) {
			killed++
		}
	}
	return killed
}

// killHolders kills every process whose environment names dir as its store
// directory — only this test's descendants do — and sweeps until several
// sweeps in a row find none, within holdersBudget. As a cleanup it runs on a
// failed test too, after the units closed and before the directory goes.
func killHolders(t *testing.T, dir string) {
	t.Helper()
	mark := []byte(storeDirEnv + "=" + dir)
	deadline := time.Now().Add(holdersBudget)
	for quiet := 0; quiet < holdersQuiet; {
		if killMarked(mark) == 0 {
			quiet++
		} else {
			quiet = 0
		}
		if !time.Now().Before(deadline) {
			t.Errorf("processes holding %s still appear after %s", dir, holdersBudget)
			return
		}
		time.Sleep(holdersPoll)
	}
}

// startProbe starts this binary with exactly env — os.StartProcess, because
// os/exec would drop a duplicate — and returns the file holding its output.
func startProbe(t *testing.T, env []string) (*os.Process, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "output")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	p, err := os.StartProcess(exe, []string{exe, "-test.run=^$"}, &os.ProcAttr{Env: env, Files: []*os.File{null, output, output}})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	return p, path
}

// awaitProbe waits for p, its own child, within probeWait.
func awaitProbe(t *testing.T, p *os.Process) *os.ProcessState {
	t.Helper()
	done := make(chan *os.ProcessState, 1)
	go func() {
		state, _ := p.Wait()
		done <- state
	}()
	select {
	case state := <-done:
		if state == nil {
			t.Fatal("the probe could not be waited for")
		}
		return state
	case <-time.After(probeWait):
		_ = p.Kill()
		<-done
		t.Fatalf("the probe ran for more than %s", probeWait)
		return nil
	}
}

// runProbe runs this binary with exactly env, to its end.
func runProbe(t *testing.T, env []string) (*os.ProcessState, string) {
	t.Helper()
	p, path := startProbe(t, env)
	state := awaitProbe(t, p)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return state, string(raw)
}

// prefillRuns records n runs under dir already.
func prefillRuns(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, helperRunsFile), bytes.Repeat([]byte{'.'}, n), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestHelperGuardRefuses: every state the guard is there for ends the helper
// with its own status, before the helper did anything; the supervisor under a
// helper is the state the fork bomb was in.
func TestHelperGuardRefuses(t *testing.T) {
	role := helperEnv + "="
	type tc struct {
		name    string
		env     []string
		prefill int
		reason  string
	}
	tests := []tc{
		{name: "a role twice", env: []string{role + "guard-probe", role + "guard-probe"}, reason: "2 occurrences"},
		{name: "a role inherited before the one set", env: []string{role + "guard-supervisor", role + "guard-probe"}, reason: "2 occurrences"},
		{name: "an unknown name", env: []string{role + "guard-nobody"}, reason: "unknown helper"},
		{name: "an empty name", env: []string{role}, reason: "unknown helper"},
		{name: "a depth and no role", env: []string{depthEnv + "=1"}, reason: "without " + helperEnv},
		{name: "past the depth", env: []string{role + "guard-probe", depthEnv + "=2"}, reason: "past 2"},
		{name: "far past the depth", env: []string{role + "guard-probe", depthEnv + "=64"}, reason: "past 2"},
		{name: "a supervisor under a helper", env: []string{role + "guard-supervisor", depthEnv + "=1"}, reason: "only a child of the test"},
		{name: "a depth nobody can read", env: []string{role + "guard-probe", depthEnv + "=deep"}, reason: "unreadable"},
		{name: "a negative depth", env: []string{role + "guard-probe", depthEnv + "=-1"}, reason: "unreadable"},
		{name: "the run past the last", env: []string{role + "guard-probe"}, prefill: maxHelperRuns, reason: "past 64"},
		{name: "a lifetime nobody can read", env: []string{role + "guard-probe", lifetimeEnv + "=soon"}, reason: "unusable"},
		{name: "a lifetime past the bound", env: []string{role + "guard-probe", lifetimeEnv + "=1h"}, reason: "unusable"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		witness := filepath.Join(dir, "witness")
		env := append([]string{witnessEnv + "=" + witness}, c.env...)
		if c.prefill > 0 {
			prefillRuns(t, dir, c.prefill)
			env = append(env, storeDirEnv+"="+dir)
		}
		state, output := runProbe(t, env)
		if state.ExitCode() != guardRefusal || !strings.Contains(output, c.reason) {
			t.Errorf("exit %d, output %q; want %d and %q", state.ExitCode(), output, guardRefusal, c.reason)
		}
		if _, err := os.Stat(witness); !os.IsNotExist(err) {
			t.Errorf("a refused helper ran: witness %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestHelperGuardRefusesABadStoreDirectory: a helper that cannot be counted
// does not run — never counted beside the test's directory.
func TestHelperGuardRefusesABadStoreDirectory(t *testing.T) {
	dir := t.TempDir()
	witness := filepath.Join(dir, "witness")
	for _, store := range []string{"relative", "", filepath.Join(dir, "absent")} {
		state, output := runProbe(t, []string{helperEnv + "=guard-probe", witnessEnv + "=" + witness, storeDirEnv + "=" + store})
		if state.ExitCode() != guardRefusal || !strings.Contains(output, "helper guard: ") {
			t.Errorf("store %q: exit %d, output %q", store, state.ExitCode(), output)
		}
		if _, err := os.Stat(witness); !os.IsNotExist(err) {
			t.Errorf("store %q: a refused helper ran: witness %v", store, err)
		}
	}
}

// TestHelperGuardAdmits: what the tests rely on still runs, at the bound.
func TestHelperGuardAdmits(t *testing.T) {
	role := helperEnv + "="
	type tc struct {
		name    string
		env     []string
		prefill int
	}
	tests := []tc{
		{name: "a helper under the test", env: []string{role + "guard-probe"}},
		{name: "a service under a supervisor", env: []string{role + "guard-probe", depthEnv + "=1"}},
		{name: "a supervisor under the test", env: []string{role + "guard-supervisor"}},
		{name: "a depth of zero", env: []string{role + "guard-supervisor", depthEnv + "=0"}},
		{name: "the last run", env: []string{role + "guard-probe"}, prefill: maxHelperRuns - 1},
		{name: "a shorter lifetime", env: []string{role + "guard-probe", lifetimeEnv + "=10s"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		witness := filepath.Join(dir, "witness")
		env := append([]string{witnessEnv + "=" + witness}, c.env...)
		if c.prefill > 0 {
			prefillRuns(t, dir, c.prefill)
			env = append(env, storeDirEnv+"="+dir)
		}
		state, output := runProbe(t, env)
		if !state.Success() {
			t.Errorf("exit %d, output %q", state.ExitCode(), output)
		}
		if _, err := os.Stat(witness); err != nil {
			t.Errorf("the helper did not run: %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestHelperGuardCountsEveryRun: each admitted run adds one to the count.
func TestHelperGuardCountsEveryRun(t *testing.T) {
	dir := t.TempDir()
	witness := filepath.Join(dir, "witness")
	for range 3 {
		state, output := runProbe(t, []string{helperEnv + "=guard-probe", witnessEnv + "=" + witness, storeDirEnv + "=" + dir})
		if !state.Success() {
			t.Fatalf("exit %d, output %q", state.ExitCode(), output)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, helperRunsFile))
	if err != nil || len(raw) != 3 {
		t.Fatalf("the count holds %q (%v), want three runs", raw, err)
	}
}

// TestHelperGuardReexecStaysAtTheSameDepth: the environment a supervisor
// re-executes itself with admits it again as a supervisor, a child of the
// test, where the plain environment it hands its services would not.
func TestHelperGuardReexecStaysAtTheSameDepth(t *testing.T) {
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv(depthEnv, "1")
	admittedDepth = 1
	t.Cleanup(func() { admittedDepth = 0 })
	state, output := runProbe(t, append(reexecEnviron("guard-supervisor"), witnessEnv+"="+witness))
	if !state.Success() {
		t.Fatalf("the re-exec's environment: exit %d, output %q", state.ExitCode(), output)
	}
	if _, err := os.Stat(witness); err != nil {
		t.Fatalf("the supervisor did not run: %v", err)
	}
	state, output = runProbe(t, append(helperEnviron("guard-supervisor"), witnessEnv+"="+witness))
	if state.ExitCode() != guardRefusal {
		t.Fatalf("a supervisor's child supervising: exit %d, output %q", state.ExitCode(), output)
	}
}

// TestHelperEnvironSetsEachNameOnce: the role and every name of extra reach
// the child once, whatever this process inherited.
func TestHelperGuardEnvironSetsEachNameOnce(t *testing.T) {
	t.Setenv(helperEnv, "handover-old")
	t.Setenv(storeDirEnv, "/inherited")
	env := helperEnviron("fdstore", storeDirEnv+"=/set")
	for name, want := range map[string]string{helperEnv: "fdstore", storeDirEnv: "/set"} {
		found := slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return entryName(kv) != name })
		if !slices.Equal(found, []string{name + "=" + want}) {
			t.Errorf("%s reached the environment as %q, want one %q", name, found, want)
		}
	}
}

// TestHelperGuardEndsAHangingHelper: a helper that never ends is killed at
// the end of its lifetime, by SIGKILL.
func TestHelperGuardEndsAHangingHelper(t *testing.T) {
	const lifetime = 300 * time.Millisecond
	begin := time.Now()
	state, output := runProbe(t, []string{helperEnv + "=guard-hang", lifetimeEnv + "=" + lifetime.String()})
	if elapsed := time.Since(begin); elapsed < lifetime || elapsed > 5*time.Second {
		t.Errorf("the helper ended after %s, want about %s", elapsed, lifetime)
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Errorf("the helper ended %v (output %q), want SIGKILL", state, output)
	}
}

// TestHelperGuardEnvironDropsTheTestTimeout: Bazel's test timeout, whatever
// this process inherited, reaches no helper, so no runner's main swallows the
// SIGTERM that stops one.
func TestHelperGuardEnvironDropsTheTestTimeout(t *testing.T) {
	t.Setenv(bazelTimeoutEnv, "300")
	for _, env := range [][]string{helperEnviron("fdstore"), reexecEnviron("guard-supervisor")} {
		if slices.ContainsFunc(env, func(kv string) bool { return entryName(kv) == bazelTimeoutEnv }) {
			t.Errorf("%s reached a helper's environment", bazelTimeoutEnv)
		}
	}
}

// TestHelperGuardKillHolders: killHolders kills the process whose environment
// names the directory and leaves the one that names another, whose path only
// begins the same.
func TestHelperGuardKillHolders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	neighbour := dir + "x"
	for _, d := range []string{dir, neighbour} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	holder, _ := startProbe(t, []string{helperEnv + "=guard-hang", storeDirEnv + "=" + dir})
	other, _ := startProbe(t, []string{helperEnv + "=guard-hang", storeDirEnv + "=" + neighbour})
	defer func() {
		_ = other.Kill()
		awaitProbe(t, other)
	}()
	killHolders(t, dir)
	status, ok := awaitProbe(t, holder).Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Errorf("the holder ended %v, want SIGKILL", status)
	}
	if err := other.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the neighbour was killed too: %v", err)
	}
}
