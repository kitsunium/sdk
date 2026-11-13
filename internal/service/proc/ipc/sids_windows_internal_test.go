//go:build windows

package ipc

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The SIDs these tests name — none needs a second account (#1349).
const (
	// usersSID and guestsSID are BUILTIN\Users and BUILTIN\Guests: SIDs the
	// DACL rows write, whoever their members are.
	usersSID  string = "S-1-5-32-545"
	guestsSID string = "S-1-5-32-546"
	// adminsSID is BUILTIN\Administrators, an owner only an elevated token
	// may give a pipe.
	adminsSID string = "S-1-5-32-544"
	// strangerSID is a well-formed SID no account of this machine has: a list
	// naming it alone names nobody the test runs as.
	strangerSID string = "S-1-5-21-1-2-3-1001"
	// daclSecurityInformation is DACL_SECURITY_INFORMATION (winnt.h).
	daclSecurityInformation uintptr = 0x00000004
	// fileCreatePipeInstance and genericRights are the bits no listed SID may
	// hold: the right to serve an instance, and any generic right that maps
	// to it.
	fileCreatePipeInstance uint32 = 0x00000004
	genericRights          uint32 = 0xF0000000
	// accessAllowedACEType is ACCESS_ALLOWED_ACE_TYPE.
	accessAllowedACEType byte = 0
	// errorInvalidOwner is ERROR_INVALID_OWNER: a token that may not make the
	// SID it names an object's owner.
	errorInvalidOwner syscall.Errno = 1307
	// settle bounds every wait on the listener's goroutine.
	settle time.Duration = 10 * time.Second
)

// CreateRestrictedToken, which only the tests call.
var procCreateRestrictedToken = modadvapi32.NewProc("CreateRestrictedToken")

// aclHeader and aceHeader are ACL and ACE_HEADER (winnt.h).
type (
	aclHeader struct {
		revision, sbz1    byte
		size, count, sbz2 uint16
	}
	aceHeader struct {
		kind, flags byte
		size        uint16
	}
)

// aceEntry is one allow entry of a DACL as the kernel holds it.
type aceEntry struct {
	sid  string
	mask uint32
}

// canonicalShape is the only text a SID is written into the DACL as.
var canonicalShape = regexp.MustCompile(`^S-1(-[0-9]+)+$`)

// daclOf is the DACL the kernel holds for the pipe h is an instance of, read
// with GetSecurityInfo — every entry an allow entry, or the test fails.
func daclOf(t *testing.T, h syscall.Handle) []aceEntry {
	t.Helper()
	var dacl *aclHeader
	var sd uintptr
	if r, _, _ := procGetSecurityInfo.Call(uintptr(h), seFileObject, daclSecurityInformation,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		t.Fatalf("GetSecurityInfo(DACL): %v", syscall.Errno(r))
	}
	defer func() {
		if _, err := syscall.LocalFree(syscall.Handle(sd)); err != nil {
			t.Logf("freeing a security descriptor: %v", err)
		}
	}()
	var out []aceEntry
	off := unsafe.Sizeof(aclHeader{})
	for range dacl.count {
		ace := unsafe.Add(unsafe.Pointer(dacl), off)
		hdr := (*aceHeader)(ace)
		if hdr.kind != accessAllowedACEType {
			t.Fatalf("an entry of type %d in the DACL", hdr.kind)
		}
		mask := *(*uint32)(unsafe.Add(ace, unsafe.Sizeof(aceHeader{})))
		sid, err := (*syscall.SID)(unsafe.Add(ace, unsafe.Sizeof(aceHeader{})+unsafe.Sizeof(mask))).String()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, aceEntry{sid: sid, mask: mask})
		off += uintptr(hdr.size)
	}
	return out
}

// enabledGroup is a group this process's token holds enabled and that does
// not stand for every caller: a SID the re-check must admit it by.
func enabledGroup(t *testing.T) string {
	t.Helper()
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer closeToken(tok)
	id, err := identityOf(tok)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range id.groups {
		if !standsForEveryCaller(g) {
			return g
		}
	}
	t.Fatalf("no enabled group in this process's token: %+v", id)
	return ""
}

// waitRefused waits until ln has refused n connections, failing the test
// after settle.
func waitRefused(t *testing.T, ln *Listener, n int64) {
	t.Helper()
	deadline := time.Now().Add(settle)
	for ln.Refused() < n {
		if time.Now().After(deadline) {
			t.Fatalf("refused %d, want %d", ln.Refused(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// acceptOnce accepts one connection on ln, closes it and reports Accept's
// error on the channel it returns.
//
// Goroutine lifecycle: one goroutine, which sends once and ends — when a
// connection is admitted, or when the listener's close ends its Accept.
func acceptOnce(ln *Listener) <-chan error {
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			err = c.Close()
		}
		done <- err
	}()
	return done
}

// The DACL a pipe is created under grants this account first and each listed
// SID clientAccess — read back from the kernel, not from the text —, writes
// this account once whether it is listed or not, and never grants a listed
// SID the right to serve an instance of the pipe, nor anything to Everyone.
func TestThePipeDACLGrantsTheListedSIDsAndNothingMore(t *testing.T) {
	self := selfAccount(t)
	for _, c := range []struct {
		name    string
		allowed []string
		want    []string
	}{
		{"this account alone", nil, []string{self}},
		{"one group", []string{usersSID}, []string{self, usersSID}},
		{"this account listed is written once", []string{self, usersSID}, []string{self, usersSID}},
		{"two groups, in the list's order", []string{usersSID, guestsSID}, []string{self, usersSID, guestsSID}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sddl := pipeSDDL(self, c.allowed)
			h, err := (&pipeAcceptor{name: pipeName(filepath.Join(t.TempDir(), "p.sock"), self), sddl: sddl}).create(true)
			if err != nil {
				t.Fatalf("creating a pipe under %q: %v", sddl, err)
			}
			defer closeHandles(h)
			aces := daclOf(t, h)
			got := make([]string, 0, len(aces))
			for _, a := range aces {
				got = append(got, a.sid)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("the DACL of %q grants %v, want %v", sddl, got, c.want)
			}
			for _, a := range aces[1:] {
				if a.mask != clientAccess || a.mask&(fileCreatePipeInstance|genericRights) != 0 {
					t.Errorf("%s is granted %#x, want %#x and never FILE_CREATE_PIPE_INSTANCE", a.sid, a.mask, clientAccess)
				}
			}
		})
	}
}

// Every entry of AllowSIDs is converted as the kernel converts a SID and
// written back in its one canonical form, once; one that is not a SID, or
// that stands for every caller, is MISCONFIGURED with the rule and the entry
// named — and nothing an entry spells beyond a SID reaches the DACL's text.
func TestAllowSIDsAreCheckedAndCanonical(t *testing.T) {
	const notASID, everyCaller = "an AllowSIDs entry is not a SID", "an AllowSIDs entry stands for every caller"
	for _, c := range []struct{ entry, want, rule string }{
		{usersSID, usersSID, ""},
		{"BU", usersSID, ""},
		{"S-1-1-0", "", everyCaller},
		{"WD", "", everyCaller},
		{"S-1-5-7", "", everyCaller},
		{"S-1-0-0", "", everyCaller},
		{"not-a-sid", "", notASID},
		{"", "", notASID},
	} {
		got, err := canonicalSIDs([]string{c.entry})
		switch {
		case c.rule == "" && (err != nil || !slices.Equal(got, []string{c.want})):
			t.Errorf("canonicalSIDs(%q) = %v, %v; want [%s]", c.entry, got, err, c.want)
		case c.rule != "" && !errs.HasCode(err, coreipc.CodeMisconfigured):
			t.Errorf("canonicalSIDs(%q) = %v, %v; want MISCONFIGURED", c.entry, got, err)
		case c.rule != "" && (fieldOf(err, "rule") != c.rule || fieldOf(err, "sid") != c.entry):
			t.Errorf("canonicalSIDs(%q): rule %q, sid %q; want %q and the entry", c.entry, fieldOf(err, "rule"), fieldOf(err, "sid"), c.rule)
		}
	}
	if got, err := canonicalSIDs([]string{usersSID, "BU", guestsSID}); err != nil || !slices.Equal(got, []string{usersSID, guestsSID}) {
		t.Errorf("a SID listed twice: %v, %v", got, err)
	}
	if got, err := canonicalSIDs([]string{usersSID + ")(A;;GA;;;WD)"}); err == nil && (len(got) != 1 || !canonicalShape.MatchString(got[0])) {
		t.Errorf("an entry spelling a second ACE was written as %q", got)
	}
}

// NewListener, NewDialer and Dial refuse a list holding an entry that is not
// a SID before anything is created: the same path then listens.
func TestAnInvalidSIDIsRefusedAtEveryEntryPoint(t *testing.T) {
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock"), AllowSIDs: []string{usersSID, "not-a-sid"}}
	if _, err := NewListener(cfg); !errs.HasCode(err, coreipc.CodeMisconfigured) || fieldOf(err, "sid") != "not-a-sid" {
		t.Errorf("NewListener: %v, want MISCONFIGURED naming the entry", err)
	}
	if _, err := NewDialer(cfg); !errs.HasCode(err, coreipc.CodeMisconfigured) {
		t.Errorf("NewDialer: %v, want MISCONFIGURED", err)
	}
	if _, err := Dial(t.Context(), cfg); !errs.HasCode(err, coreipc.CodeMisconfigured) {
		t.Errorf("Dial: %v, want MISCONFIGURED", err)
	}
	cfg.AllowSIDs = []string{usersSID}
	ln, err := NewListener(cfg)
	if err != nil {
		t.Fatalf("after the refusals: %v", err)
	}
	closeBestEffort(ln, cfg.Path)
}

// The listener reads the client's token again after the DACL: the client —
// this account, which the DACL grants — is admitted by its user or one of its
// enabled groups only when the list names it, once the listener stands as
// LocalSystem; otherwise it is closed and counted, and Accept returns nothing.
func TestTheListenerReadsTheClientsTokenAgain(t *testing.T) {
	self := selfAccount(t)
	group := enabledGroup(t)
	for _, c := range []struct {
		name     string
		allowed  []string
		admitted bool
	}{
		{"its user listed", []string{self}, true},
		{"one of its enabled groups listed", []string{group}, true},
		{"neither listed", []string{strangerSID}, false},
		{"nothing listed", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
			ln, err := listenAs(cfg, self, nil)
			if err != nil {
				t.Fatal(err)
			}
			//: the DACL grants this account; the admission now stands for
			//: another one, and names the client by the list alone.
			ln.selfSID, ln.cfg.AllowSIDs = otherAccount, c.allowed
			accepted := acceptOnce(ln)
			conn, err := Dial(t.Context(), cfg)
			if err != nil {
				closeBestEffort(ln, cfg.Path)
				t.Fatalf("dial: %v", err)
			}
			defer closeBestEffort(conn, cfg.Path)
			if c.admitted {
				select {
				case err := <-accepted:
					if err != nil {
						t.Fatalf("accept: %v", err)
					}
				case <-time.After(settle):
					t.Fatal("an admitted client was not returned by Accept")
				}
			} else {
				waitRefused(t, ln, 1)
			}
			if err := ln.Close(); err != nil {
				t.Error(err)
			}
			if !c.admitted {
				if err := <-accepted; !errs.HasCode(err, coreipc.CodeClosed) {
					t.Errorf("Accept after a refused client and Close: %v, want CLOSED", err)
				}
			}
			want := int64(1)
			if c.admitted {
				want = 0
			}
			if ln.Refused() != want {
				t.Errorf("refused %d, want %d", ln.Refused(), want)
			}
		})
	}
}

// A client whose token cannot be read — one that opened the pipe at
// SECURITY_ANONYMOUS, whose token OpenThreadToken never opens — is refused
// and counted, never admitted as the DACL's.
func TestAClientWhoseTokenCannotBeReadIsRefused(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	ln, err := NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptOnce(ln)
	p, err := syscall.UTF16PtrFromString(string(pipeName(cfg.Path, self)))
	if err != nil {
		t.Fatal(err)
	}
	//: SECURITY_SQOS_PRESENT with SECURITY_ANONYMOUS, which is 0.
	h, err := syscall.CreateFile(p, clientAccess, 0, nil, syscall.OPEN_EXISTING, securitySQOSPresent, 0)
	if err != nil {
		closeBestEffort(ln, cfg.Path)
		t.Fatalf("an anonymous open: %v", err)
	}
	defer closeHandles(h)
	waitRefused(t, ln, 1)
	if err := ln.Close(); err != nil {
		t.Error(err)
	}
	if err := <-accepted; !errs.HasCode(err, coreipc.CodeClosed) {
		t.Errorf("Accept after an anonymous client and Close: %v, want CLOSED", err)
	}
}

// restrictedToken is CreateRestrictedToken of tok, the SIDs of disable made
// deny-only and those of restrict its restricting SIDs; it is closed at the
// end of the test.
func restrictedToken(t *testing.T, tok syscall.Token, disable, restrict []syscall.SIDAndAttributes) syscall.Token {
	t.Helper()
	first := func(s []syscall.SIDAndAttributes) *syscall.SIDAndAttributes {
		if len(s) == 0 {
			return nil
		}
		return &s[0]
	}
	var out syscall.Token
	if r, _, err := procCreateRestrictedToken.Call(uintptr(tok), 0, uintptr(len(disable)), uintptr(unsafe.Pointer(first(disable))),
		0, 0, uintptr(len(restrict)), uintptr(unsafe.Pointer(first(restrict))), uintptr(unsafe.Pointer(&out))); r == 0 {
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	t.Cleanup(func() { closeToken(out) })
	return out
}

// The re-check judges a restricted token as the kernel's two passes judge
// it: a user made deny-only grants nothing, an enabled group still does, and
// a token with restricting SIDs is admitted only when one of them is named
// too. Proven on tokens made from this process's own, without a second
// account.
func TestARestrictedTokenIsJudgedAsTheKernelJudgesIt(t *testing.T) {
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var tok syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY|syscall.TOKEN_DUPLICATE, &tok); err != nil {
		t.Fatal(err)
	}
	defer closeToken(tok)
	id, err := identityOf(tok)
	if err != nil {
		t.Fatal(err)
	}
	self, group := id.user, enabledGroup(t)
	if !id.admittedBy(self, nil) || id.admittedBy(otherAccount, nil) {
		t.Fatalf("this process's own token: %+v", id)
	}

	u, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	denyUser, err := identityOf(restrictedToken(t, tok, []syscall.SIDAndAttributes{{Sid: u.User.Sid}}, nil))
	switch {
	case err != nil:
		t.Fatal(err)
	case !denyUser.userDenyOnly || denyUser.user != self:
		t.Errorf("the user made deny-only reads %+v", denyUser)
	case denyUser.admittedBy(self, nil):
		t.Error("a deny-only user was admitted as this account")
	case !denyUser.admittedBy(self, []string{group}):
		t.Errorf("an enabled group %s, listed, did not admit the token", group)
	}

	stranger, err := syscall.StringToSid(strangerSID)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := identityOf(restrictedToken(t, tok, nil, []syscall.SIDAndAttributes{{Sid: stranger}}))
	switch {
	case err != nil:
		t.Fatal(err)
	case !slices.Equal(restricted.restricting, []string{strangerSID}):
		t.Errorf("restricting SIDs %v, want [%s]", restricted.restricting, strangerSID)
	case restricted.admittedBy(self, nil):
		t.Error("a token restricted to a SID nobody lists was admitted")
	case !restricted.admittedBy(self, []string{strangerSID}):
		t.Error("a token restricted to a listed SID was refused")
	}
}

// A dial whose own account's pipe does not exist reaches the pipe of a listed
// account — here this one, the dial standing as LocalSystem —, judged by its
// owner and by the process that serves it, both that account: verified.
func TestADialReachesAListedAccountsPipe(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	ln, err := NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBestEffort(ln, cfg.Path)
	accepted := acceptOnce(ln)
	conn, err := dialAs(t.Context(), cfg, otherAccount, []string{self})
	if err != nil {
		t.Fatalf("dial to a listed account's pipe: %v", err)
	}
	defer closeBestEffort(conn, cfg.Path)
	if !conn.Peer.Verified || conn.Peer.SID != self {
		t.Errorf("the listener as the dial sees it: %+v", conn.Peer)
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
	case <-time.After(settle):
		t.Fatal("Accept did not return the client")
	}
	if _, err := dialAs(t.Context(), cfg, strangerSID, []string{otherAccount}); !errs.HasCode(err, coreipc.CodeDialFailed) {
		t.Errorf("a dial where no pipe of the order exists: %v, want DIAL_FAILED", err)
	}
}

// A listed account's pipe that another account owns — what a squatter that
// computed the name leaves — is ENDPOINT_FOREIGN, the owner named; and a name
// that exists and is refused ends the dial: a later account's pipe, here a
// live listener of this account the dial would have been admitted by, is
// never stepped over to.
func TestADialRefusesAListedAccountsPipeAnotherOwns(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	squatPipe(t, pipeName(cfg.Path, otherAccount), "O:"+self+"D:P(A;;GA;;;"+self+")")
	_, err := dialAs(t.Context(), cfg, strangerSID, []string{otherAccount})
	if !errs.HasCode(err, coreipc.CodeEndpointForeign) || fieldOf(err, "owner") != self {
		t.Fatalf("a listed account's pipe this account owns: %v, want ENDPOINT_FOREIGN naming %s", err, self)
	}

	ln, err := NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBestEffort(ln, cfg.Path)
	squatPipe(t, pipeName(cfg.Path, strangerSID), "O:"+self+"D:P(A;;GA;;;"+self+")")
	if _, err := dialAs(t.Context(), cfg, strangerSID, []string{self}); !errs.HasCode(err, coreipc.CodeEndpointForeign) || fieldOf(err, "sid") != self {
		t.Errorf("a refused first name with a live listed listener after it: %v, want ENDPOINT_FOREIGN from the first name", err)
	}
}

// A listed account's pipe owned by that account but served by a process of
// another is ENDPOINT_FOREIGN, the server's account named. Only a token that
// may make Administrators an owner can stage it: elsewhere the creation's own
// refusal is asserted, and the test skips.
func TestADialRefusesAListedAccountsPipeServedByAnother(t *testing.T) {
	self := selfAccount(t)
	cfg := &Config{Path: filepath.Join(t.TempDir(), "p.sock")}
	h, err := (&pipeAcceptor{name: pipeName(cfg.Path, adminsSID), sddl: "O:" + adminsSID + "D:P(A;;GA;;;" + self + ")"}).create(true)
	if errors.Is(err, errorInvalidOwner) {
		t.Skipf("this token may not make Administrators an owner (%v): an elevated run stages the row", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles(h)
	_, err = dialAs(t.Context(), cfg, strangerSID, []string{adminsSID})
	if !errs.HasCode(err, coreipc.CodeEndpointForeign) || fieldOf(err, "sid") != self {
		t.Fatalf("a pipe Administrators own, served as %s: %v, want ENDPOINT_FOREIGN naming the server", self, err)
	}
}
