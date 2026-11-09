//go:build windows

package ipc

import (
	"path/filepath"
	"strings"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// otherAccount is an account this test never runs as: LocalSystem.
const otherAccount string = "S-1-5-18"

// selfAccount is the account the test runs as.
func selfAccount(t *testing.T) string {
	t.Helper()
	self, err := accountOf(0)
	if err != nil {
		t.Fatal(err)
	}
	return self
}

// squatPipe creates the first instance of name under sddl, as a squatter
// that made the name first would, and closes it at the end of the test.
func squatPipe(t *testing.T, name pipeAddr, sddl string) {
	t.Helper()
	h, err := (&pipeAcceptor{name: name, sddl: sddl}).create(true)
	if err != nil {
		t.Fatalf("pre-creating %s under %q: %v", name, sddl, err)
	}
	t.Cleanup(func() { closeHandles(h) })
}

// fieldOf is the string field key of err, empty when it carries none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// The pipe's name carries the account it is this account's for, so two
// accounts with one path never meet on one name — and the descriptor names
// that account as the owner, not the token's default one.
func TestThePipeNameCarriesTheAccount(t *testing.T) {
	self := selfAccount(t)
	path := filepath.Join(t.TempDir(), "p.sock")
	name := string(pipeName(path, self))
	if !strings.Contains(name, self) {
		t.Errorf("pipe %q does not carry the account %q", name, self)
	}
	if !strings.HasPrefix(name, pipePrefix) || !strings.HasSuffix(name, "-p.sock") {
		t.Errorf("pipe %q", name)
	}
	if other := string(pipeName(path, otherAccount)); other == name {
		t.Errorf("two accounts share the pipe %q", name)
	}
	if got := pipeSDDL(self); !strings.HasPrefix(got, "O:"+self+"D:P(") {
		t.Errorf("descriptor %q does not name %q as its owner", got, self)
	}
}

// A pipe made first under a descriptor that admits no instance of this
// account — what a squatter of another account leaves — is refused as
// squatted, never as IN_USE, and the listener's name is not joined.
func TestAPipeThatAdmitsNoInstanceOfOursIsSquatted(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	squatPipe(t, pipeName(cfg.Path, self), "D:P(A;;GR;;;WD)")
	ln, err := NewListener(cfg)
	if err == nil {
		closeBestEffort(ln, "a listener the test should not have got")
		t.Fatal("listened on a squatted name")
	}
	if !errs.HasCode(err, coreipc.CodeEndpointSquatted) || errs.HasCode(err, coreipc.CodeInUse) {
		t.Fatalf("listen on a squatted name = %v, want ENDPOINT_SQUATTED", err)
	}
	if fieldOf(err, "cause") == "" {
		t.Errorf("no cause in %v", err)
	}
}

// A pipe another account owns is refused as squatted even when its DACL lets
// this account join it, and the owner field names who holds it. The pipe is
// made by this process, so the account it is judged for is LocalSystem's:
// to that listener, the pipe is another account's.
func TestAPipeAnotherAccountOwnsIsSquatted(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	squatPipe(t, pipeName(cfg.Path, otherAccount), "O:"+self+"D:P(A;;GA;;;WD)")
	ln, err := listenAs(cfg, otherAccount)
	if err == nil {
		closeBestEffort(ln, "a listener the test should not have got")
		t.Fatal("listened on a name another account holds")
	}
	if !errs.HasCode(err, coreipc.CodeEndpointSquatted) || errs.HasCode(err, coreipc.CodeInUse) {
		t.Fatalf("listen on another account's pipe = %v, want ENDPOINT_SQUATTED", err)
	}
	if got := fieldOf(err, "owner"); got != self {
		t.Errorf("owner field %q, want %q", got, self)
	}
}

// The owner read from a pipe this account made is this account, which is
// what makes a second listener of ours IN_USE rather than squatted.
func TestThePipeOwnerIsTheAccount(t *testing.T) {
	self := selfAccount(t)
	h, err := (&pipeAcceptor{name: pipeName(filepath.Join(t.TempDir(), "p.sock"), self), sddl: pipeSDDL(self)}).create(true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles(h)
	owner, err := ownerOf(h)
	if err != nil || owner != self {
		t.Fatalf("owner %q, %v; want %q", owner, err, self)
	}
}
