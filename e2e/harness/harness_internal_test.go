// Package harness — the conformance runner's internals.
package harness

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// failingWriter is an io.Writer that always refuses, so the report's
// best-effort printing can be exercised.
type failingWriter struct {
	// writes counts how many times printing was attempted.
	writes int
}

// Write implements io.Writer by failing.
func (f *failingWriter) Write(p []byte) (n int, err error) {
	f.writes++
	//: a full disk or a closed pipe looks exactly like this.
	return 0, errors.New("write refused")
}

// Test_safeRun pins CONTAINMENT: a check that panics is recorded as a Fail, and
// the run continues.
//
// A conformance binary that died on the first misbehaving check would report
// nothing about every domain after it — so the one check that is broken would
// hide however many are also broken, and the run would have to be repeated once
// per defect.
func Test_safeRun(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// check is the probe under test.
		check Check
		// wantStatus is the outcome that must be recorded.
		wantStatus Status
		// wantName is the check name the Result must carry.
		wantName string
	}
	tests := []tc{
		{
			name:       "a check that returns",
			check:      func() Result { return Passed("codec", "roundtrip", "ok") },
			wantStatus: Pass, wantName: "roundtrip",
		},
		{
			name:       "a check that fails",
			check:      func() Result { return Failed("codec", "roundtrip", "bad") },
			wantStatus: Fail, wantName: "roundtrip",
		},
		{
			//: the panic is contained and attributed, so the table shows which
			//: domain misbehaved rather than nothing at all.
			name:       "a check that panics with a string",
			check:      func() Result { panic("check exploded") },
			wantStatus: Fail, wantName: "panic",
		},
		{
			name:       "a check that panics with an error",
			check:      func() Result { panic(errors.New("check exploded")) },
			wantStatus: Fail, wantName: "panic",
		},
		{
			//: a nil dereference is what a real check panics with, and it is not
			//: a string.
			name: "a check that dereferences nil",
			check: func() Result {
				var r *Result
				return *r
			},
			wantStatus: Fail, wantName: "panic",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := safeRun("codec", c.check)

		//: reaching this line at all is the containment property.
		if got.Status != c.wantStatus {
			t.Fatalf("Status = %v, want %v", got.Status, c.wantStatus)
		}
		if got.Name != c.wantName {
			t.Errorf("Name = %q, want %q", got.Name, c.wantName)
		}
		//: the domain is carried by the group, so a panicking check is still
		//: attributed to the domain it was registered under.
		if got.Domain != "codec" {
			t.Errorf("Domain = %q, want the registering domain", got.Domain)
		}
		//: a contained panic must say what it was, or the row is unactionable.
		if c.wantName == "panic" && got.Detail == "" {
			t.Error("the contained panic carried no detail")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// hangingCheckUntil is a check that blocks until release is closed — a stand-in
// for the sd_notify or cgroup check that ran until e2e-vm's 25-minute cap. The
// suite closes release at cleanup, so the abandoned goroutine does not outlive
// the test that planted it.
func hangingCheckUntil(release <-chan struct{}) Check {
	return func() Result {
		<-release
		//: only reached once the test lets go.
		return Passed("sdnotify", "late", "released")
	}
}

// Test_watchedRun pins the watchdog (#118): a check that does not return in time
// is a Fail named by its position, and every goroutine's stack is written out —
// the stuck function among them — while a check that returns in time keeps its
// own verdict. Before it, a hung check left a job cancelled at its cap with no
// line of output, because the table is printed only after every check returns.
func Test_watchedRun(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// hang is whether the check blocks past the bound.
		hang bool
		// wantStatus is the recorded outcome.
		wantStatus Status
		// wantName is the recorded check name.
		wantName string
		// wantDump is whether a goroutine dump must be written.
		wantDump bool
	}
	tests := []tc{
		{name: "a check that returns in time", wantStatus: Pass, wantName: "roundtrip"},
		{name: "a check that hangs", hang: true, wantStatus: Fail, wantName: "check 2 of 3", wantDump: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		check := func() Result { return Passed("sdnotify", "roundtrip", "ok") }
		if c.hang {
			check = hangingCheckUntil(release)
		}
		var out bytes.Buffer

		got := watchedRun(&out, "sdnotify", "check 2 of 3", check, 50*time.Millisecond)

		if got.Status != c.wantStatus || got.Name != c.wantName {
			t.Fatalf("watchedRun = %v %q, want %v %q", got.Status, got.Name, c.wantStatus, c.wantName)
		}
		if got.Domain != "sdnotify" {
			t.Errorf("Domain = %q, want the registering domain", got.Domain)
		}
		dumped := strings.Contains(out.String(), "goroutine ")
		if dumped != c.wantDump {
			t.Fatalf("goroutine dump written = %v, want %v:\n%s", dumped, c.wantDump, out.String())
		}
		//: the dump is only worth writing if it names the function that is stuck.
		if c.wantDump && !strings.Contains(out.String(), "hangingCheckUntil") {
			t.Errorf("the dump does not name the stuck check:\n%s", out.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_runWithin pins that a hung check costs its own row and nothing else: the
// checks after it still run, and the run's exit code counts it.
func Test_runWithin(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// wantFails is the count Run returns.
		wantFails int
	}
	tests := []tc{{name: "one hung check among three", wantFails: 1}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		ran := false
		groups := []CheckGroup{{Domain: "sdnotify", Checks: []Check{
			func() Result { return Passed("sdnotify", "a", "ok") },
			hangingCheckUntil(release),
			func() Result {
				ran = true
				return Passed("sdnotify", "c", "ok")
			},
		}}}
		var out bytes.Buffer

		fails := runWithin(&out, groups, 50*time.Millisecond)

		if fails != c.wantFails {
			t.Fatalf("runWithin = %d fails, want %d:\n%s", fails, c.wantFails, out.String())
		}
		if !ran {
			t.Fatal("the check after the hung one never ran")
		}
		if !strings.Contains(out.String(), "check 2 of 3") {
			t.Errorf("the table does not name the hung check:\n%s", out.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_report pins the tally, which is what the summary line and the exit code
// are both built from.
//
// Only Fail counts. Miscounting an Unsupported would make every platform without
// a mechanic exit non-zero for behaving as documented; miscounting a Skip would
// do the same for an image with no shell.
func Test_report(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// statuses are the outcomes reported, in order.
		statuses []Status
		// wantFails is the count that becomes the exit code.
		wantFails int
	}
	tests := []tc{
		{name: "nothing ran"},
		{name: "every check passed", statuses: []Status{Pass, Pass, Pass}},
		{name: "one failure", statuses: []Status{Pass, Fail, Pass}, wantFails: 1},
		{name: "several failures", statuses: []Status{Fail, Fail}, wantFails: 2},
		//: neither of these counts against the host.
		{name: "off-platform and skipped", statuses: []Status{Unsupported, Skip}},
		{name: "a mix of every outcome", statuses: []Status{Pass, Fail, Unsupported, Skip}, wantFails: 1},
		{
			//: an unknown status falls into the skip bucket rather than being
			//: silently counted as a failure.
			name: "an unrecognised status", statuses: []Status{Status("WAT")},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		results := make([]Result, 0, len(c.statuses))
		for i, status := range c.statuses {
			results = append(results, Result{
				Domain: "codec", Name: string(rune('a' + i)), Status: status, Detail: "d",
			})
		}
		var out strings.Builder

		fails := report(&out, results)

		if fails != c.wantFails {
			t.Fatalf("report = %d fails, want %d — the count is the process exit code",
				fails, c.wantFails)
		}
		//: one row per result, plus a blank line and the summary.
		rows := strings.Count(out.String(), "\n") - 2
		if rows != len(c.statuses) {
			t.Fatalf("the table has %d rows for %d results:\n%s",
				rows, len(c.statuses), out.String())
		}
		if !strings.Contains(out.String(), "summary:") {
			t.Errorf("the report has no summary line:\n%s", out.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stderrf pins that a write fault does NOT change the verdict.
//
// The report is diagnostic output; the process exit code is the contract. A
// conformance run whose table could not be printed — a closed pipe, a full disk
// — still knows whether the host conforms, and turning that into a crash would
// lose the one thing worth having.
func Test_stderrf(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// failing sends the report to a writer that always refuses.
		failing bool
		// format and args are what is printed.
		format string
		args   []any
	}
	tests := []tc{
		{name: "a plain line", format: "%s\n", args: []any{"hello"}},
		{name: "a table row", format: "%-11s %-11s %s\n", args: []any{"codec", "PASS", "roundtrip"}},
		{name: "a writer that refuses", failing: true, format: "%s\n", args: []any{"hello"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.failing {
			out := &failingWriter{}

			//: reaching the assertion below at all is the property: a refused
			//: write must not panic and must not propagate.
			stderrf(out, c.format, c.args...)

			if out.writes == 0 {
				t.Fatal("the report never attempted to write")
			}
			return
		}
		var out strings.Builder

		stderrf(&out, c.format, c.args...)

		if out.Len() == 0 {
			t.Fatal("the report wrote nothing to a working writer")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
