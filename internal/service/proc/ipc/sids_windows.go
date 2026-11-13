//go:build windows

package ipc

import (
	"errors"
	"log"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The rights a listed SID is granted, and the token attributes of winnt.h
// this file reads (#1349).
const (
	// clientAccess is FILE_GENERIC_READ — READ_CONTROL, SYNCHRONIZE,
	// FILE_READ_DATA, FILE_READ_ATTRIBUTES, FILE_READ_EA — and FILE_WRITE_DATA:
	// what a client needs to read, write and learn who owns and serves the
	// pipe, and what Dial opens it with. Not FILE_GENERIC_WRITE: its
	// FILE_APPEND_DATA is, on a pipe, FILE_CREATE_PIPE_INSTANCE, the right to
	// serve an instance of the pipe and so to pose as its listener.
	clientAccess uint32 = 0x0012008B
	// clientAccessSDDL is clientAccess as an SDDL access right.
	clientAccessSDDL string = "0x12008b"
	// seGroupEnabled and seGroupDenyOnly are SE_GROUP_ENABLED and
	// SE_GROUP_USE_FOR_DENY_ONLY: a group counts in an access check only when
	// enabled, a deny-only SID never grants.
	seGroupEnabled  uint32 = 0x00000004
	seGroupDenyOnly uint32 = 0x00000010
	// tokenInfoSize is the first buffer a token's list is read into; a token
	// with more says how much it needs.
	tokenInfoSize uint32 = 512
	// tokenInfoTries bounds the reads of one list, each sized to the last
	// answer.
	tokenInfoTries int = 4
	// openAsSelf is OpenThreadToken's TRUE: the access check made as this
	// process, the only way to open a token held at SECURITY_IDENTIFICATION.
	openAsSelf uintptr = 1
	// currentThread is GetCurrentThread's pseudo-handle, (HANDLE)-2.
	currentThread uintptr = ^uintptr(1)
	// errorBadLength is ERROR_BAD_LENGTH, which some token classes answer to
	// a buffer too small.
	errorBadLength syscall.Errno = 24
)

// The entry points that read a pipe client's token through the pipe.
var (
	procImpersonatePipeClient = modadvapi32.NewProc("ImpersonateNamedPipeClient")
	procOpenThreadToken       = modadvapi32.NewProc("OpenThreadToken")
	procRevertToSelf          = modadvapi32.NewProc("RevertToSelf")
)

// credentials are what the Windows admission reads beside a client's
// PeerValue: the identity its token speaks for, nil when it cannot be read.
type credentials = *tokenIdentity

// tokenIdentity is who a token speaks for, as the kernel's access check reads
// it.
type tokenIdentity struct {
	// user is the token's user; userDenyOnly is true when a restricted token
	// made it deny-only, and then it grants nothing.
	user         string
	userDenyOnly bool
	// groups are the token's groups that are enabled and not deny-only.
	groups []string
	// restricting are a restricted token's restricting SIDs, none for any
	// other token.
	restricting []string
}

// tokenSID is one SID of a token's list, as text, with its attributes.
type tokenSID struct {
	sid   string
	attrs uint32
}

// tokenGroups is TOKEN_GROUPS (winnt.h): a count, then that many
// SID_AND_ATTRIBUTES, at the pointer alignment Go lays the array out at.
type tokenGroups struct {
	count  uint32
	groups [1]syscall.SIDAndAttributes
}

// standsForEveryCaller reports whether sid is one no pipe's DACL may name:
// Everyone, Anonymous Logon or the Null SID, any caller, authenticated or not.
func standsForEveryCaller(sid string) bool {
	switch sid {
	case "S-1-1-0", "S-1-5-7", "S-1-0-0":
		return true
	}
	return false
}

// canonicalSIDs is the list as the kernel writes SIDs — each entry converted
// with ConvertStringSidToSid and back —, once each. That form is what a
// token's SIDs are compared with and the only one written into the DACL, so
// nothing an entry spells reaches the descriptor's text. An entry that is not
// a SID, or that stands for every caller, is MISCONFIGURED, the entry named.
func canonicalSIDs(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, entry := range list {
		sid, err := canonicalSID(entry)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, sid) {
			out = append(out, sid)
		}
	}
	return out, nil
}

// canonicalSID is one entry of AllowSIDs as the kernel writes it, or the
// refusal that names it.
func canonicalSID(entry string) (string, error) {
	sid, err := syscall.StringToSid(entry)
	var text string
	if err == nil {
		text, err = sid.String()
	}
	switch {
	case err != nil:
		return "", errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "an AllowSIDs entry is not a SID"),
			errs.String("sid", entry), errs.String("cause", err.Error()))
	case standsForEveryCaller(text):
		return "", errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "an AllowSIDs entry stands for every caller"),
			errs.String("sid", entry))
	}
	return text, nil
}

// checkSIDs refuses a list canonicalSIDs refuses.
func checkSIDs(sids []string) error {
	_, err := canonicalSIDs(sids)
	return err
}

// pipeSDDL is the descriptor the pipe is created under: owned by the account
// sid — never the token's default owner, Administrators for an elevated
// process, so a later listen reads the account itself —, a DACL granting it
// everything, and each listed SID clientAccess, protected from inheritance.
// No other entry is written: nobody the list does not name, Everyone first.
func pipeSDDL(sid string, allowed []string) string {
	var b strings.Builder
	b.WriteString("O:" + sid + "D:P(A;;GA;;;" + sid + ")")
	for _, s := range allowed {
		if s != sid {
			b.WriteString("(A;;" + clientAccessSDDL + ";;;" + s + ")")
		}
	}
	return b.String()
}

// admittedBy says whether a client speaking for id may use the pipe of
// account self opened to allowed, as the kernel judged its open: its user —
// unless deny-only — or one of its enabled groups is self or listed, and,
// for a restricted token, so is one of its restricting SIDs, the kernel's
// second pass over the DACL with those SIDs alone.
func (id *tokenIdentity) admittedBy(self string, allowed []string) bool {
	named := func(sid string) bool { return sid == self || slices.Contains(allowed, sid) }
	byUser := !id.userDenyOnly && named(id.user)
	if !byUser && !slices.ContainsFunc(id.groups, named) {
		return false
	}
	return len(id.restricting) == 0 || slices.ContainsFunc(id.restricting, named)
}

// identityOf reads tok's user, its enabled groups and its restricting SIDs;
// a token any of them cannot be read from is no identity.
func identityOf(tok syscall.Token) (tokenIdentity, error) {
	u, err := tok.GetTokenUser()
	if err != nil {
		return tokenIdentity{}, err
	}
	user, err := u.User.Sid.String()
	if err != nil {
		return tokenIdentity{}, err
	}
	groups, err := tokenSIDs(tok, syscall.TokenGroups)
	if err != nil {
		return tokenIdentity{}, err
	}
	restricting, err := tokenSIDs(tok, syscall.TokenRestrictedSids)
	if err != nil {
		return tokenIdentity{}, err
	}
	id := tokenIdentity{user: user, userDenyOnly: u.User.Attributes&seGroupDenyOnly != 0}
	for _, g := range groups {
		if g.attrs&seGroupEnabled != 0 && g.attrs&seGroupDenyOnly == 0 {
			id.groups = append(id.groups, g.sid)
		}
	}
	for _, r := range restricting {
		id.restricting = append(id.restricting, r.sid)
	}
	return id, nil
}

// tokenSIDs reads the list class names in tok — TokenGroups or
// TokenRestrictedSids, both a TOKEN_GROUPS — as text. A count the buffer
// cannot hold is refused, never read past.
func tokenSIDs(tok syscall.Token, class uint32) ([]tokenSID, error) {
	buf, err := tokenInfo(tok, class)
	if err != nil {
		return nil, err
	}
	tg := (*tokenGroups)(unsafe.Pointer(&buf[0]))
	n := int(tg.count)
	if n == 0 {
		return nil, nil
	}
	if uintptr(len(buf)) < unsafe.Offsetof(tg.groups)+uintptr(n)*unsafe.Sizeof(tg.groups[0]) {
		return nil, syscall.ERROR_INSUFFICIENT_BUFFER
	}
	out := make([]tokenSID, 0, n)
	for _, g := range unsafe.Slice(&tg.groups[0], n) {
		sid, err := g.Sid.String()
		if err != nil {
			return nil, err
		}
		out = append(out, tokenSID{sid: sid, attrs: g.Attributes})
	}
	//: the SIDs point into buf, which must outlive their conversion.
	runtime.KeepAlive(buf)
	return out, nil
}

// tokenInfo is what GetTokenInformation reads of class from tok, in a buffer
// grown to what the token says it needs and never smaller than a
// TOKEN_GROUPS, so the count is always inside it.
func tokenInfo(tok syscall.Token, class uint32) ([]byte, error) {
	n := tokenInfoSize
	for range tokenInfoTries {
		buf := make([]byte, max(n, uint32(unsafe.Sizeof(tokenGroups{}))))
		err := syscall.GetTokenInformation(tok, class, &buf[0], uint32(len(buf)), &n)
		if err == nil {
			return buf, nil
		}
		tooSmall := errors.Is(err, syscall.ERROR_INSUFFICIENT_BUFFER) || errors.Is(err, errorBadLength)
		if !tooSmall || n <= uint32(len(buf)) {
			return nil, err
		}
	}
	return nil, syscall.ERROR_INSUFFICIENT_BUFFER
}

// clientIdentity is who the client of h is, read from the token the pipe
// captured when the client opened it — bound to this connection, never to a
// pid that may since name another process. The thread acts as the client
// only for the OpenThreadToken, on a goroutine locked to its OS thread; a
// thread that could not stop acting as the client is never handed back to
// the runtime, which ends it with the goroutine, and the client is refused.
func clientIdentity(h syscall.Handle) (tokenIdentity, error) {
	type read struct {
		id  tokenIdentity
		err error
	}
	done := make(chan read, 1)
	//: one goroutine per read, which sends once and ends; Accept waits for it.
	go func() {
		runtime.LockOSThread()
		tok, revertErr, err := clientToken(h)
		if revertErr != nil {
			//: still acting as the client: the thread ends with this goroutine.
			done <- read{err: revertErr}
			return
		}
		runtime.UnlockOSThread()
		if err != nil {
			done <- read{err: err}
			return
		}
		id, err := identityOf(tok)
		closeToken(tok)
		done <- read{id: id, err: err}
	}()
	r := <-done
	return r.id, r.err
}

// clientToken opens, as this process (openAsSelf), the token the thread holds
// once ImpersonateNamedPipeClient made it act as the client of h, and reverts
// the thread at once. revertErr is set only when the thread could not stop
// acting as the client; err says why no token was opened.
func clientToken(h syscall.Handle) (tok syscall.Token, revertErr, err error) {
	if r, _, e := procImpersonatePipeClient.Call(uintptr(h)); r == 0 {
		//: a failed impersonation leaves the thread as it was; reverting all
		//: the same costs nothing and proves it.
		return 0, revertToSelf(), e
	}
	r, _, openErr := procOpenThreadToken.Call(currentThread, uintptr(syscall.TOKEN_QUERY), openAsSelf, uintptr(unsafe.Pointer(&tok)))
	if revertErr := revertToSelf(); revertErr != nil {
		if r != 0 {
			closeToken(tok)
		}
		return 0, revertErr, nil
	}
	if r == 0 {
		return 0, nil, openErr
	}
	return tok, nil, nil
}

// revertToSelf ends the thread's impersonation.
func revertToSelf() error {
	if r, _, err := procRevertToSelf.Call(); r == 0 {
		return err
	}
	return nil
}

// closeToken closes a token this package no longer needs; a close that fails
// is logged, as closeHandles logs a handle's.
func closeToken(tok syscall.Token) {
	if err := tok.Close(); err != nil {
		log.Printf("ipc: closing a client token: %v", err)
	}
}
