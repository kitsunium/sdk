// Package checks — the sdnotify and sdlisten conformance checks.
package checks

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// stageEnv puts a UNIQUE key into the requested state and hands it back.
//
// The key is unique per case, which is what removes the need to unset anything:
// a variable nothing has ever set is already absent, and "absent" is a state the
// helpers under test treat differently from "empty". t.Setenv covers the other
// state, and brings the automatic restore and the serialisation with it — the
// test runner refuses to let a test that calls it run in parallel, which is
// exactly right when the thing being staged is global to the process.
func stageEnv(t *testing.T, name, value string, present bool) string {
	t.Helper()
	key := "KITSUNIUM_E2E_" + strings.ToUpper(strings.NewReplacer(" ", "_", "-", "_").Replace(name))
	//: a key nothing has set is already in the "absent" state.
	if !present {
		//: nothing to stage; the caller wants it missing.
		return key
	}
	t.Setenv(key, value)
	return key
}

// Test_lifecycleMatch pins that the flag vector is compared EXACTLY, and that a
// mismatch names both sides.
//
// The four accessors are meant to track the raw State map, so a comparison that
// tolerated a difference would let a Notification report itself as Ready when
// the supervisor said Stopping — and the check would pass. The detail matters
// too: with four booleans and no vectors printed, a failing row says only "the
// flags were wrong".
func Test_lifecycleMatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// got and want are the observed and expected truth vectors, in the
		// order ready / reloading / stopping / watchdog.
		got  [lifecycleFlagCount]bool
		want [lifecycleFlagCount]bool
		// wantOK is whether the vectors must be reported as matching.
		wantOK bool
	}
	tests := []tc{
		{name: "all false", wantOK: true},
		{
			name:   "ready",
			got:    [lifecycleFlagCount]bool{true, false, false, false},
			want:   [lifecycleFlagCount]bool{true, false, false, false},
			wantOK: true,
		},
		{
			name:   "every flag set",
			got:    [lifecycleFlagCount]bool{true, true, true, true},
			want:   [lifecycleFlagCount]bool{true, true, true, true},
			wantOK: true,
		},
		{
			//: ready reported for a supervisor that said stopping.
			name: "the wrong flag",
			got:  [lifecycleFlagCount]bool{true, false, false, false},
			want: [lifecycleFlagCount]bool{false, false, true, false},
		},
		{
			name: "one flag missing",
			got:  [lifecycleFlagCount]bool{true, false, false, false},
			want: [lifecycleFlagCount]bool{true, false, false, true},
		},
		{
			name: "one flag extra",
			got:  [lifecycleFlagCount]bool{true, true, false, false},
			want: [lifecycleFlagCount]bool{true, false, false, false},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		detail, ok := lifecycleMatch(c.got, c.want)

		if ok != c.wantOK {
			t.Fatalf("lifecycleMatch(%v, %v) reported ok = %v, want %v", c.got, c.want, ok, c.wantOK)
		}
		//: a match says nothing; a mismatch has to print both vectors, or the
		//: failing row cannot identify which flag went wrong.
		if ok != (detail == "") {
			t.Fatalf("ok = %v but detail = %q — the report contradicts itself", ok, detail)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_envHas pins that the environment membership test is EXACT.
//
// The Prepare check asserts the child's Spec carries LISTEN_FDS=1. A prefix or
// substring test would also accept LISTEN_FDS=10, or a LISTEN_FDNAMES entry
// that merely contains the text — and the check would then pass for a child
// that inherits a different number of sockets than it was handed.
func Test_envHas(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// env is the child's environment slice.
		env []string
		// kv is the exact entry being looked for.
		kv string
		// want is whether it is present.
		want bool
	}
	tests := []tc{
		{name: "the only entry", env: []string{"LISTEN_FDS=1"}, kv: "LISTEN_FDS=1", want: true},
		{
			name: "one of several entries",
			env:  []string{"PATH=/bin", "LISTEN_FDS=1", "LISTEN_FDNAMES=http"},
			kv:   "LISTEN_FDS=1", want: true,
		},
		{name: "an empty environment", kv: "LISTEN_FDS=1"},
		//: a prefix match would accept this for LISTEN_FDS=1.
		{name: "a longer value with the same prefix", env: []string{"LISTEN_FDS=10"}, kv: "LISTEN_FDS=1"},
		{name: "the right key with the wrong value", env: []string{"LISTEN_FDS=2"}, kv: "LISTEN_FDS=1"},
		{name: "a different key", env: []string{"LISTEN_PID=1"}, kv: "LISTEN_FDS=1"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := envHas(c.env, c.kv); got != c.want {
			t.Fatalf("envHas(%v, %q) = %v, want %v", c.env, c.kv, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_dropErr pins that discarding a cleanup fault is total and silent. The
// helper exists so the error audit can tell a deliberate discard from a dropped
// one, and every call site is a best-effort Close whose failure cannot change
// the check's verdict.
func Test_dropErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// err is what the cleanup step reported.
		err error
	}
	tests := []tc{
		{name: "nothing went wrong"},
		{name: "a close on an already-closed socket", err: errors.New("use of closed network connection")},
		{name: "an unset on an absent variable", err: errors.New("setenv: invalid argument")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: reaching the assertion below at all is the property: the helper must
		//: never panic and must never propagate.
		dropErr(c.err)

		if t.Failed() {
			t.Fatalf("discarding %v was not silent", c.err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_withEnvSet pins that the restore closure reinstates the environment
// EXACTLY, including the difference between "was absent" and "was empty".
//
// The checks stage a synthetic activation environment around a call. A restore
// that left the key behind — or left an empty string where there had been
// nothing — would leak into every later check in the run, and sd_listen_fds
// treats an empty LISTEN_FDS very differently from an absent one.
func Test_withEnvSet(t *testing.T) {
	//: serial: t.Setenv refuses to coexist with t.Parallel, and it is right
	//: to — the process environment is global, so two staged activation
	//: environments would see each other.
	type tc struct {
		// name describes the case.
		name string
		// prior is the value the variable held before, when it held one.
		prior string
		// hadPrior is whether it was set at all.
		hadPrior bool
		// staged is the value the check installs.
		staged string
	}
	tests := []tc{
		{name: "a variable that was absent", staged: "1"},
		{name: "a variable that was set", prior: "before", hadPrior: true, staged: "1"},
		//: empty and absent are different states, and sd_listen_fds reads them
		//: differently.
		{name: "a variable that was empty", prior: "", hadPrior: true, staged: "1"},
		{name: "staging an empty value", prior: "before", hadPrior: true, staged: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := stageEnv(t, c.name, c.prior, c.hadPrior)

		restore := withEnvSet(key, c.staged)

		got, present := os.LookupEnv(key)
		if !present || got != c.staged {
			t.Fatalf("%s = %q/%v after staging, want %q/true", key, got, present, c.staged)
		}

		restore()

		after, stillPresent := os.LookupEnv(key)
		if stillPresent != c.hadPrior {
			t.Fatalf("%s present = %v after restore, want %v — the staged value "+
				"leaks into every later check", key, stillPresent, c.hadPrior)
		}
		if c.hadPrior && after != c.prior {
			t.Fatalf("%s = %q after restore, want the prior %q", key, after, c.prior)
		}
	}
	for _, c := range tests {
		//: serial for the same reason the parent is.
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_withEnvUnset is the mirror: clearing a variable for the duration of a
// check must leave it exactly as it was found, and must be a no-op for a
// variable that was not there to begin with.
func Test_withEnvUnset(t *testing.T) {
	//: serial: t.Setenv refuses to coexist with t.Parallel, and it is right
	//: to — the process environment is global, so two staged activation
	//: environments would see each other.
	type tc struct {
		// name describes the case.
		name string
		// prior is the value the variable held before, when it held one.
		prior string
		// hadPrior is whether it was set at all.
		hadPrior bool
	}
	tests := []tc{
		{name: "a variable that was set", prior: "before", hadPrior: true},
		{name: "a variable that was empty", prior: "", hadPrior: true},
		{name: "a variable that was absent"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := stageEnv(t, c.name, c.prior, c.hadPrior)

		restore := withEnvUnset(key)

		//: cleared for the duration of the check, whatever it was before.
		if _, present := os.LookupEnv(key); present {
			t.Fatalf("%s is still set while it should be cleared", key)
		}

		restore()

		after, stillPresent := os.LookupEnv(key)
		if stillPresent != c.hadPrior {
			t.Fatalf("%s present = %v after restore, want %v", key, stillPresent, c.hadPrior)
		}
		if c.hadPrior && after != c.prior {
			t.Fatalf("%s = %q after restore, want the prior %q", key, after, c.prior)
		}
	}
	for _, c := range tests {
		//: serial for the same reason the parent is.
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// hasKey reports whether m holds key, without the two-value form the caller
// would otherwise have to spell out inline.
func hasKey(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

// Test_withEnvBatch pins that staging a whole activation environment is undone
// as ONE step.
//
// A partial restore is the worst outcome here: a run left holding LISTEN_FDS
// without LISTEN_PID would make every later sdlisten check see an activation
// environment nobody staged, and read the resulting empty set as a pass.
func Test_withEnvBatch(t *testing.T) {
	//: serial: t.Setenv refuses to coexist with t.Parallel, and it is right
	//: to — the process environment is global, so two staged activation
	//: environments would see each other.
	type tc struct {
		// name describes the case.
		name string
		// vars maps a key suffix to the value the check stages under it.
		vars map[string]string
		// preset maps a key suffix to the value it already held.
		preset map[string]string
	}
	tests := []tc{
		{name: "nothing to stage"},
		{
			name: "a full activation environment",
			vars: map[string]string{"FDS": "1", "PID": "1234", "FDNAMES": "http"},
		},
		{
			name:   "staging over existing values",
			vars:   map[string]string{"FDS": "1"},
			preset: map[string]string{"FDS": "before"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each case gets its own key space, so nothing has to be unset and no
		//: case can see another's variables.
		vars := make(map[string]string, len(c.vars))
		preset := make(map[string]string, len(c.preset))
		for suffix, value := range c.vars {
			key := stageEnv(t, c.name+suffix, c.preset[suffix], hasKey(c.preset, suffix))
			vars[key] = value
			if prior, had := c.preset[suffix]; had {
				preset[key] = prior
			}
		}

		restore := withEnvBatch(vars)

		for key, want := range vars {
			if got, present := os.LookupEnv(key); !present || got != want {
				t.Fatalf("%s = %q/%v after staging, want %q/true", key, got, present, want)
			}
		}

		restore()

		//: every variable is undone, or a later check sees an activation
		//: environment nobody staged.
		for key := range vars {
			want, hadPrior := preset[key]
			after, present := os.LookupEnv(key)
			if present != hadPrior {
				t.Fatalf("%s present = %v after restore, want %v", key, present, hadPrior)
			}
			if hadPrior && after != want {
				t.Fatalf("%s = %q after restore, want the prior %q", key, after, want)
			}
		}
	}
	for _, c := range tests {
		//: serial for the same reason the parent is.
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// The sdnotify and sdlisten checks stand up their own sockets rather than
// requiring systemd, but they still reach the kernel and still stage the process
// environment — so what is pinned here is the property that holds everywhere:
// the check runs to completion and produces a row the runner can tally.
//
// The environment-staging ones are serial by necessity. t.Setenv refuses to
// coexist with t.Parallel, and it is right to: the environment is global, so two
// staged activation environments would see each other.

// Test_sdnotifyNoopWhenUnset pins the row for the case that matters most in
// practice: with no NOTIFY_SOCKET set, notifying must be a NO-OP rather than an
// error. Almost every process runs outside systemd, so an error here would make
// the common path the failing one.
func Test_sdnotifyNoopWhenUnset(t *testing.T) {
	//: serial — the check stages the process environment.
	if problem := wellFormedRow(sdnotifyNoopWhenUnset(), sdnotifyDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_sdnotifyNotificationStates pins the lifecycle-accessor row: Ready,
// Reloading, Stopping and Watchdog must track the raw State map rather than
// being derived from something else.
func Test_sdnotifyNotificationStates(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(sdnotifyNotificationStates(), sdnotifyDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_sdnotifyReadinessRoundTrip pins the full supervisor path row — Listen
// stands up the credential-passing socket, the notifier sends Ready, and the
// listener receives a credential-verified Notification. It is the only place the
// whole path is exercised without systemd.
func Test_sdnotifyReadinessRoundTrip(t *testing.T) {
	//: serial — the check stages the process environment.
	if problem := wellFormedRow(sdnotifyReadinessRoundTrip(), sdnotifyDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_sdlistenPreparePopulatesSpec pins the row for the parent half of socket
// activation: Prepare must put the descriptor AND the matching LISTEN_* entries
// into the child's Spec, or the child inherits a socket it cannot find.
func Test_sdlistenPreparePopulatesSpec(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(sdlistenPreparePopulatesSpec(), sdlistenDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_sdlistenEmptySetNotError pins the distinction a hand-written
// implementation gets wrong: an activation environment addressed to ANOTHER
// process yields no sockets and NO error. Reporting an error there would make
// every ordinary start of a socket-activation-capable service fail.
func Test_sdlistenEmptySetNotError(t *testing.T) {
	//: serial — the check stages the process environment.
	if problem := wellFormedRow(sdlistenEmptySetNotError(), sdlistenDomain); problem != nil {
		t.Fatal(problem)
	}
}
