// Internal tests for head.go - git package.
package git

import (
	"runtime"
	"testing"
	"time"
)

// Test_committerTime pins the parse of a raw commit object: the committer
// line's last two fields, after the email's closing bracket, in the offset
// they were recorded with — and nothing from the message body.
func Test_committerTime(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		commit     string
		want       time.Time
		wantOffset int
		wantOK     bool
	}
	plain := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author A U Thor <a@example.com> 1727165000 +0000\n" +
		"committer C O Mitter <c@example.com> 1727170800 +0200\n\nsubject\n"
	signed := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author A <a@example.com> 1 +0000\n" +
		"committer Name With <Many> Words? <c@example.com> 1727170800 -0530\n" +
		"gpgsig -----BEGIN SSH SIGNATURE-----\n committer Fake <f@example.com> 1 +0000\n -----END SSH SIGNATURE-----\n\nbody\n"
	body := "tree x\nauthor A <a@example.com> 1 +0000\n\ncommitter In <the@body> 99 +0000\n"
	tests := []tc{
		{name: "a plain commit", commit: plain, want: time.Unix(1727170800, 0), wantOffset: 2 * 3600, wantOK: true},
		{
			//: the LAST '>' ends the email, and a signature's continuation line
			//: starts with a space, so neither can be mistaken for the header.
			name: "a signed commit with a bracket in the name", commit: signed,
			want: time.Unix(1727170800, 0), wantOffset: -(5*3600 + 30*60), wantOK: true,
		},
		{name: "a committer line in the message is not the header", commit: body},
		{name: "no committer line", commit: "tree x\n\nmessage\n"},
		{name: "no email", commit: "committer Nobody 1727170800 +0200\n"},
		{name: "a malformed offset", commit: "committer C <c@example.com> 1727170800 +2\n"},
		{name: "an offset with minutes past sixty", commit: "committer C <c@example.com> 1727170800 +0275\n"},
		{name: "a malformed timestamp", commit: "committer C <c@example.com> soon +0200\n"},
		{name: "an extra field", commit: "committer C <c@example.com> 1 +0200 extra\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := committerTime(c.commit)
		if ok != c.wantOK {
			t.Fatalf("committerTime() ok = %v, want %v (got %v)", ok, c.wantOK, got)
		}
		if !ok {
			return
		}
		if !got.Equal(c.want) {
			t.Errorf("committerTime() = %v, want %v", got, c.want)
		}
		if _, offset := got.Zone(); offset != c.wantOffset {
			t.Errorf("offset = %d, want %d", offset, c.wantOffset)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHeadRefusesRepositoryControlledExecution pins that the status Head runs
// is the hardened one. `git status` is exactly where core.fsmonitor fires, so
// a planted key would otherwise run on a query the caller believes is
// read-only — the vector hardenedGitConfig exists for.
//
// Unix-only: the payload is a /bin/sh script.
func TestHeadRefusesRepositoryControlledExecution(t *testing.T) {
	t.Parallel()
	//: The payload is a POSIX shell script.
	if runtime.GOOS == "windows" {
		t.Log("skipping: the planted payload is a /bin/sh script")
		return
	}
	repo, evidence := plantHostileRepo(t, "core.fsmonitor")
	head, err := Head(t.Context(), repo)
	if err != nil {
		t.Fatalf("Head() = %v, want nil", err)
	}
	//: the fixture dirties a tracked file, so a working status says so.
	if !head.Modified {
		t.Error("Head() read a dirty tree as clean; the hardening broke the status")
	}
	if n := executionCount(t, evidence); n != 0 {
		t.Errorf("planted payload executed %d time(s)", n)
	}
}
