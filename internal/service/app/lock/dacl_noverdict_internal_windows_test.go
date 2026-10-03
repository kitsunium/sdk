//go:build windows

// Package lock — every answer the DACL reader gives WITHOUT a verdict names
// what stopped it, so a caller can tell "could not look" from "looked, and
// nobody may write". The lock accepts the first (ADR 0084 §D5); the queue,
// asking the same reader, refuses it (ADR 0095).
package lock

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryAnswerWithoutAVerdictIsNamed pins the reader's two answers a test
// can cause for real — a path UTF-16 cannot carry, a directory
// GetNamedSecurityInfoW cannot open — against the one answer that IS a
// verdict: a list read to the end, which alone comes back with nothing to name.
//
// The third no-verdict answer, an entry GetAce cannot fetch, is named the same
// way ("GetAce#N") but has no fixture: the lists this file's siblings assemble
// are well formed, and one that lies about its size would be read past its end.
func TestEveryAnswerWithoutAVerdictIsNamed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// path builds the directory handed to the reader.
		path func(t *testing.T) string
		// wantPrefix is how observed must begin; "" wants it empty.
		wantPrefix string
	}
	tests := []tc{
		{"a path with a NUL in it", func(t *testing.T) string { t.Helper(); return t.TempDir() + "\x00x" }, "UTF16PtrFromString="},
		{"a directory that is not there", func(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "absent") }, "GetNamedSecurityInfoW="},
		{"a list read to the end", func(t *testing.T) string { t.Helper(); return t.TempDir() }, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		granted, observed := dirGrantsAnyone(c.path(t), replaceRights, contentRights)
		//: none of these grants anything to anybody.
		if granted {
			t.Fatalf("%s: granted, observed=%q", c.name, observed)
		}
		//: a verdict names nothing; the absence of one always names its cause.
		if c.wantPrefix == "" {
			//: empty, exactly.
			if observed != "" {
				t.Fatalf("%s: observed = %q, want empty — a verdict was reached", c.name, observed)
			}
			return
		}
		//: the named cause.
		if !strings.HasPrefix(observed, c.wantPrefix) {
			t.Fatalf("%s: observed = %q, want it to begin %q", c.name, observed, c.wantPrefix)
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
