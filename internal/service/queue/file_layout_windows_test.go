//go:build windows

// Package queue_test — the durable broker's directory rules on Windows, where
// they read the DACL (dirtrust_windows.go) instead of a mode the platform does
// not have, and where the broker then meets vfs's platform refusal.
//
// Every row drives the real Windows access-control model through `icacls` and
// `mklink /J`, which ship with the operating system and need no privilege on a
// directory the caller owns. A row whose ACL or junction cannot be applied is a
// FAILURE, never a skip: `go test` runs here without -v, so a skipped row and a
// passing one are the same green tick (lock's dacl_windows_test.go carries the
// same rule, for the same reason).
package queue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// The string-form SIDs icacls is handed, so the tests and the rule agree on the
// identifier by construction rather than by a name a non-English locale would
// resolve differently.
const (
	sidEveryone       string = "*S-1-1-0"
	sidAdministrators string = "*S-1-5-32-544"
)

// icaclsGrant applies an icacls permission string to path for one identifier,
// and FAILS rather than skips when it cannot.
func icaclsGrant(t *testing.T, path, sid, permission string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "icacls", path, "/grant", sid+":"+permission).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls could not grant %q to %s on %s — a row that cannot be planted is a failure, not a skip: %s (%v)", permission, sid, path, out, err)
	}
}

// TestTheWindowsQueueDirectoryRuleIsTheDACL pins the two rules on Windows, the
// accepting rows as firmly as the refusing ones.
//
// An accepted directory does not yield a broker here: internal/service/vfs
// refuses Windows by design, so a directory the rules accept is one NewFile
// answers with UNSUPPORTED_PLATFORM — and that answer is exactly what proves
// the rules accepted it, where the old mode rule refused every directory as
// QUEUE_DIRECTORY_UNUSABLE.
//
// The row that separates the two rules is "Everyone may add entries": the root
// accepts it, since it only lets a stranger create an entry and nothing lives
// at the root but the states, and the ready/ the broker then creates INHERITS
// that grant — on Unix a state is made 0700 whatever its parent allows, here it
// takes the parent's list — so the state refuses it, as a planted message.
func TestTheWindowsQueueDirectoryRuleIsTheDACL(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// sid and grant are applied to the queue directory; an empty grant
		// leaves its ACL exactly as created.
		sid, grant string
		// refusedAt is "" for an accepted directory, "root" for a refusal of the
		// queue directory, or the state the refusal names.
		refusedAt string
	}
	tests := []tc{
		{name: "as created, no broad entry at all"},
		{name: "Everyone may only read", sid: sidEveryone, grant: "(OI)(CI)R"},
		{name: "a group share: Administrators may write", sid: sidAdministrators, grant: "(OI)(CI)F"},
		{name: "Everyone may delete an entry it did not create", sid: sidEveryone, grant: "(CI)(DC)", refusedAt: "root"},
		{name: "Everyone may rewrite the list", sid: sidEveryone, grant: "(CI)(WDAC)", refusedAt: "root"},
		{name: "Everyone may add entries, which ready/ then inherits", sid: sidEveryone, grant: "(CI)(WD,AD)", refusedAt: "ready"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "queue")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("Mkdir(%s) = %v", root, err)
		}
		if c.grant != "" {
			icaclsGrant(t, root, c.sid, c.grant)
		}
		broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: root, Policy: defaultPolicy()})
		if broker != nil {
			t.Fatal("NewFile returned a broker on windows")
		}
		assertWindowsVerdict(t, err, c.refusedAt, "world-writable")
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAWindowsStateDirectoryIsCheckedWhoeverMadeIt pins the state rule on
// directories somebody else put there first — the injection the root rule
// cannot see, since creating ready/ is exactly what a stranger with the right
// to add an entry can do.
//
// The junction is the row no DACL can see: it points at a private, perfectly
// acceptable directory, and os.Lstat reports a junction as a plain directory,
// so only the reparse-point attribute tells the rule it is an indirection that
// keeps the messages wherever its author chose.
func TestAWindowsStateDirectoryIsCheckedWhoeverMadeIt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		state string
		why   string
		plant func(t *testing.T, statePath string)
	}
	tests := []tc{
		{"a ready/ Everyone may write", "ready", "world-writable", func(t *testing.T, statePath string) {
			t.Helper()
			mkdir(t, statePath)
			icaclsGrant(t, statePath, sidEveryone, "(OI)(CI)W")
		}},
		{"a ready/ whose files Everyone may write, and nothing on the directory", "ready", "world-writable", func(t *testing.T, statePath string) {
			t.Helper()
			mkdir(t, statePath)
			icaclsGrant(t, statePath, sidEveryone, "(OI)(IO)(W)")
		}},
		{"an inflight/ that is a junction to a private directory", "inflight", "symlink", func(t *testing.T, statePath string) {
			t.Helper()
			elsewhere := filepath.Join(t.TempDir(), "elsewhere")
			mkdir(t, elsewhere)
			out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", statePath, elsewhere).CombinedOutput()
			if err != nil {
				t.Fatalf("mklink /J needs no privilege, so a failure is a change in the runner and not a skip: %s (%v)", out, err)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "queue")
		mkdir(t, root)
		c.plant(t, filepath.Join(root, c.state))
		broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: root, Policy: defaultPolicy()})
		if broker != nil {
			t.Fatal("NewFile returned a broker on windows")
		}
		assertWindowsVerdict(t, err, c.state, c.why)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertWindowsVerdict pins what NewFile answered on Windows: the platform
// refusal for a directory the rules accepted, or QUEUE_DIRECTORY_UNUSABLE
// naming the state (none, for the root), the reason and — since there is no
// mode to point at — the identifier and rights the verdict rests on.
func assertWindowsVerdict(t *testing.T, err error, refusedAt, why string) {
	t.Helper()
	if refusedAt == "" {
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("NewFile = %v, want the platform's UNSUPPORTED_PLATFORM: the directory rules had nothing to refuse", err)
		}
		return
	}
	if !errs.HasCode(err, svcqueue.CodeQueueDirectoryUnusable) {
		t.Fatalf("NewFile = %v, want QUEUE_DIRECTORY_UNUSABLE refused at %s", err, refusedAt)
	}
	wantState := refusedAt
	if refusedAt == "root" {
		wantState = ""
	}
	if got := fieldValue(err, "state"); got != wantState {
		t.Errorf("state field = %q, want %q", got, wantState)
	}
	if got := fieldValue(err, "why"); got != why {
		t.Errorf("why field = %q, want %q", got, why)
	}
	//: a DACL verdict names who holds what; a shape verdict has nothing to name.
	if why == "world-writable" && !strings.HasPrefix(fieldValue(err, "observed"), "S-1-1-0=") {
		t.Errorf("observed field = %q, want the Everyone entry the refusal rests on", fieldValue(err, "observed"))
	}
}

// mkdir creates dir, failing the test when it cannot.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir(%s) = %v", dir, err)
	}
}
