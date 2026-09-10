//go:build unix

// Package sdlisten — socket activation is a startup mechanism, so the honest
// question is not "how fast" but "is any of it accidentally quadratic or
// accidentally on a hot path". The environment parsing is pure string work a
// caller could in principle re-run; the activator side dups a descriptor per
// listener, which is a syscall the protocol requires and the SDK cannot avoid.
// Both are measured, and the dup is measured separately so it can be subtracted
// from Prepare rather than blamed on it.
package sdlisten

import (
	"net"
	"os"
	"strconv"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Sinks defeat dead-code elimination on the value-returning helpers.
var (
	errSink   error
	intSink   int
	boolSink  bool
	strSink   []string
	envSink   []string
	fileSink  *os.File
	filesSink []*os.File
)

// ── the pure half: parsing the activation environment ────────────────────────

// BenchmarkFdCount_Absent is the path a NON-activated process takes: one
// environment lookup and an immediate "nothing inherited". Every binary that
// links this package and is not socket-activated pays exactly this, once.
func BenchmarkFdCount_Absent(b *testing.B) {
	b.Setenv(envFds, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n, ok, err := fdCount()
		intSink, boolSink, errSink = n, ok, err
	}
}

// BenchmarkFdCount_Present is the activated path: parse LISTEN_FDS, then verify
// LISTEN_PID against getpid(2). The gap against the row above is that syscall.
func BenchmarkFdCount_Present(b *testing.B) {
	b.Setenv(envFds, "4")
	b.Setenv(envPID, strconv.Itoa(os.Getpid()))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n, ok, err := fdCount()
		intSink, boolSink, errSink = n, ok, err
	}
}

// BenchmarkPidMatches_Absent is the trusted-parent shortcut: an unset LISTEN_PID
// is accepted without a getpid(2), because our own activator cannot know the
// child pid pre-fork.
func BenchmarkPidMatches_Absent(b *testing.B) {
	b.Setenv(envPID, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = pidMatches()
	}
}

// BenchmarkPidMatches_Present pays the getpid(2) the shortcut above avoids.
func BenchmarkPidMatches_Present(b *testing.B) {
	b.Setenv(envPID, strconv.Itoa(os.Getpid()))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = pidMatches()
	}
}

func BenchmarkSplitNames_Unset(b *testing.B) {
	b.Setenv(envFdNames, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = splitNames(4)
	}
}

func BenchmarkSplitNames_Four(b *testing.B) {
	b.Setenv(envFdNames, "http:https:metrics:admin")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = splitNames(4)
	}
}

// BenchmarkSplitNames_Padded is the ragged case the protocol permits: fewer
// names than descriptors, so the remainder is padded with the unnamed "".
func BenchmarkSplitNames_Padded(b *testing.B) {
	b.Setenv(envFdNames, "http:https")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = splitNames(8)
	}
}

// BenchmarkEnvWithout_64 is the activator's environment rewrite: Prepare drops
// the three activation variables before re-setting them, and this is the only
// unbounded loop it runs. slices.Contains over three keys per entry is linear in
// both, so this row exists to confirm the product stays small at a realistic
// environment size.
func BenchmarkEnvWithout_64(b *testing.B) {
	env := benchEnv(64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		envSink = envWithout(env, envFds, envPID, envFdNames)
	}
}

// ── the syscall half: the activator's per-listener dup ───────────────────────

// BenchmarkListenerFile_Dup is Prepare's per-listener cost in isolation: one
// File() that dup(2)s the socket, plus the Close that keeps the benchmark from
// exhausting the descriptor table. Subtract N of these from a Prepare row to see
// what the SDK's own bookkeeping costs.
func BenchmarkListenerFile_Dup(b *testing.B) {
	lns := benchListeners(b, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		f, err := listenerFile(lns["s0"])
		if err == nil {
			if cErr := f.Close(); cErr != nil {
				b.Fatalf("Close: %v", cErr)
			}
		}
		fileSink, errSink = f, err
	}
}

// BenchmarkPrepare_One and BenchmarkPrepare_Four measure the whole activator
// call: sort the names, dup one socket per listener, prepend them to
// ExtraFiles, and rewrite the child's activation environment. The dup'd
// descriptors are closed inside the timed region — Prepare hands ownership to
// the caller, and a loop that kept them would run out of descriptors rather than
// produce a number.
func BenchmarkPrepare_One(b *testing.B) {
	benchPrepare(b, 1)
}

func BenchmarkPrepare_Four(b *testing.B) {
	benchPrepare(b, 4)
}

// BenchmarkPrepare_Empty is the no-op: no listeners means the child spec is left
// untouched, and this row proves that costs nothing.
func BenchmarkPrepare_Empty(b *testing.B) {
	spec := coreproc.Spec{Path: "/bin/true"}
	named := map[string]net.Listener{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = Prepare(&spec, named)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// benchPrepare runs Prepare over n listeners per iteration, releasing the dup'd
// descriptors it produced so the loop can run indefinitely.
func benchPrepare(b *testing.B, n int) {
	b.Helper()
	lns := benchListeners(b, n)
	env := benchEnv(16)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		spec := coreproc.Spec{Path: "/bin/true", Env: env}
		errSink = Prepare(&spec, lns)
		for _, f := range spec.ExtraFiles {
			if cErr := f.Close(); cErr != nil {
				b.Fatalf("Close: %v", cErr)
			}
		}
		filesSink = spec.ExtraFiles
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("Prepare: %v", errSink)
	}
}

// benchListeners binds n loopback TCP listeners and registers their teardown.
// TCP rather than Unix-domain so the benchmark needs no private directory and
// so listenerFile takes the same *net.TCPListener branch a real activator does.
func benchListeners(b *testing.B, n int) map[string]net.Listener {
	b.Helper()
	out := make(map[string]net.Listener, n)
	for i := range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Skipf("cannot bind a loopback listener: %v", err)
		}
		closeAfter(b, ln)
		out["s"+strconv.Itoa(i)] = ln
	}
	return out
}

// closeAfter registers ln's teardown. It takes the listener as a parameter
// rather than closing over a loop variable, so the closure captures a value the
// caller owns instead of the loop's own binding.
func closeAfter(b *testing.B, ln net.Listener) {
	b.Helper()
	b.Cleanup(func() {
		if cErr := ln.Close(); cErr != nil {
			b.Logf("listener Close: %v", cErr)
		}
	})
}

// benchEnv builds an n-entry environment of realistic shape, built once outside
// every timed loop.
func benchEnv(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "KITSU_BENCH_VAR_"+strconv.Itoa(i)+"=value-"+strconv.Itoa(i))
	}
	return out
}
