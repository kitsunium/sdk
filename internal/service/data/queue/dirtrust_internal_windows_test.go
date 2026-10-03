//go:build windows

// Package queue — the Windows verdict on each answer the DACL reader can give,
// the "no verdict" one included, pinned where no real ACL produces every answer
// on demand.
package queue

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAWindowsDirectoryNobodyCouldInspectIsRefused pins dirVerdict's answers.
//
// The reader returns granted=false both for a list it read to the end and for
// one it could not read, or read only in part; only observed tells them apart.
// The lock accepts the second (ADR 0084 §D5). The queue refuses it: a queue
// directory wrongly accepted is one a stranger may plant a message in, and the
// queue refused every directory on Windows before this rule existed, so the
// refusal takes away nothing that worked (ADR 0095).
func TestAWindowsDirectoryNobodyCouldInspectIsRefused(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// granted and found are the reader's answer.
		granted bool
		found   string
		// wantWhy, wantObserved and wantUnusable are the verdict.
		wantWhy, wantObserved string
		wantUnusable          bool
	}
	tests := []tc{
		{"read to the end, and nobody may write", false, "", "", "", false},
		{"Everyone may take an entry away", true, "S-1-1-0=0x40", "world-writable", "S-1-1-0=0x40", true},
		{"the list could not be read", false, "GetNamedSecurityInfoW=5", "unverifiable", "GetNamedSecurityInfoW=5", true},
		{"the walk stopped at an entry", false, "GetAce#3", "unverifiable", "GetAce#3", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		why, observed, unusable := dirVerdict(c.granted, c.found)
		//: every part of the verdict, since each carries a different fact.
		if why != c.wantWhy || observed != c.wantObserved || unusable != c.wantUnusable {
			t.Fatalf("dirVerdict(%v, %q) = (%q, %q, %v), want (%q, %q, %v)",
				c.granted, c.found, why, observed, unusable, c.wantWhy, c.wantObserved, c.wantUnusable)
		}
	}
	//: one subtest per answer.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAWindowsDirectoryWhoseListCannotBeReadIsRefused drives both rules over a
// REAL failure of the reader: GetNamedSecurityInfoW on a directory that is not
// there. It is the one failure a test can cause on demand. A list made
// unreadable with icacls — READ_CONTROL denied to Everyone and to OWNER
// RIGHTS — was still read on the windows-latest runner (measured, ADR 0095),
// so that fixture cannot be relied on to exist.
func TestAWindowsDirectoryWhoseListCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		rule func(dir string) (why, observed string, unusable bool)
	}
	tests := []tc{
		{"the queue directory's rule", func(dir string) (string, string, bool) { return rootWritableByAnyone(dir, nil) }},
		{"a state directory's rule", func(dir string) (string, string, bool) { return stateWritableByAnyone(dir, nil) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		why, observed, unusable := c.rule(filepath.Join(t.TempDir(), "absent"))
		//: refused, and for the reason that no verdict was reached.
		if !unusable || why != "unverifiable" {
			t.Fatalf("%s over an unreadable list = (%q, %q, %v), want refused as unverifiable", c.name, why, observed, unusable)
		}
		//: carrying the Win32 status the reader could not get past.
		if !strings.HasPrefix(observed, "GetNamedSecurityInfoW=") {
			t.Errorf("observed = %q, want the reader's GetNamedSecurityInfoW status", observed)
		}
	}
	//: one subtest per rule.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
