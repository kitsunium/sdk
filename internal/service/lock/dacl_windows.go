//go:build windows

// Package lock — "could anybody create an entry in this directory?", asked of
// Windows in the only vocabulary Windows has for it.
//
// # Why the POSIX question has no answer here, and this one does
//
// checkDir and checkChain both rest on one question: is this directory one any
// account can write? On Unix that is a single mode bit. On Windows os.Stat has
// no permission bits to read — it SYNTHESISES a mode from
// FILE_ATTRIBUTE_READONLY, so every writable directory reports 0777 and every
// read-only one reports 0555 — so the POSIX rule would refuse every directory
// a caller could name, which is ADR 0018 §(a)'s failure mode wearing an error
// that blames the deployment (ADR 0081 §D5).
//
// The question itself is perfectly answerable on this platform; it is just not
// a mode. It is the directory's discretionary access control list: is there an
// access-allowed entry granting a right that lets a stranger CREATE a file
// there, to a security identifier that means "anybody"?
//
// # The cost estimate that deferred this twice, re-checked
//
// ADR 0081 §Alternatives priced a DACL check at roughly 250 lines of ABI and
// deferred it; ADR 0082 §Deferred carried that forward unchanged. Reading
// go1.27's syscall package rather than assuming shrinks it to two entry points,
// because the standard library already exports everything else this needs:
//
//   - syscall.StringToSid builds a SID from its string form, so the well-known
//     identifiers cost nothing (security_windows.go, ConvertStringSidToSidW);
//   - (*syscall.SID).String renders one back, so the comparison is a string
//     comparison and EqualSid is not needed at all;
//   - syscall.LocalFree releases the security descriptor.
//
// What is left is GetNamedSecurityInfoW and GetAce, bound from advapi32.dll
// with syscall.NewLazyDLL — the same discipline ADR 0081 used to bind
// LockFileEx from kernel32, and for the same reason: golang.org/x/sys is
// banned SDK-wide (ADR 0018) and binding two stable exports adds no module, no
// go.sum entry and no MODULE.bazel change.
//
// # It fails OPEN, and that is deliberate
//
// Every failure on the way to a verdict — a path that cannot be converted, an
// object whose security information cannot be read, an ACE that cannot be
// fetched — answers "no, not writable by anybody". A wrong REFUSAL here costs
// a caller a locker that will never build on a directory that is perfectly
// safe; a wrong acceptance costs the hardening this file adds and leaves the
// platform exactly where it was before. Those are not symmetric, and the
// asymmetry decides.
package lock

import (
	"strconv"
	"syscall"
	"unsafe"
)

// advapi32 entry points for reading a directory's DACL, bound lazily and
// resolved on first Call. Both have been exported since Windows 2000.
var (
	modadvapi32 = syscall.NewLazyDLL("advapi32.dll")
	// GetNamedSecurityInfoW retrieves a named object's security descriptor.
	// It returns a Win32 error code as its RESULT rather than through
	// GetLastError, so the r1 of the call is the verdict and the lastErr
	// beside it is meaningless.
	procGetNamedSecurityInfo = modadvapi32.NewProc("GetNamedSecurityInfoW")
	// GetAce hands back a pointer INTO the ACL — it copies nothing, so the
	// security descriptor must outlive every ACE read from it.
	procGetAce = modadvapi32.NewProc("GetAce")
)

// seFileObject is SE_OBJECT_TYPE's SE_FILE_OBJECT (accctrl.h): a file or
// directory named by a path.
const seFileObject uintptr = 1

// daclSecurityInformation is DACL_SECURITY_INFORMATION (winnt.h). Only the
// discretionary list is asked for — the owner, the group and the audit list
// are not part of this question, and SACL_SECURITY_INFORMATION additionally
// needs a privilege an ordinary account does not hold.
const daclSecurityInformation uintptr = 0x00000004

// errorSuccess is ERROR_SUCCESS, the value GetNamedSecurityInfoW returns when
// it succeeded.
const errorSuccess uintptr = 0

// ACE types (winnt.h). Only the two that grant and deny discretionary access
// are consulted; the object variants carry a GUID between the mask and the
// SID, so their layout differs and they are deliberately not decoded.
const (
	accessAllowedAceType byte = 0x00
	accessDeniedAceType  byte = 0x01
)

// inheritOnlyAce is INHERIT_ONLY_ACE (winnt.h): the entry describes what
// children inherit and grants nothing on the object itself, so it is skipped.
const inheritOnlyAce byte = 0x08

// Specific rights that let their holder put an entry into a directory, or take
// the ability to (winnt.h).
const (
	// fileAddFile is FILE_ADD_FILE — create a file in this directory.
	fileAddFile uint32 = 0x0002
	// fileAddSubdirectory is FILE_ADD_SUBDIRECTORY.
	fileAddSubdirectory uint32 = 0x0004
	// fileWriteEA is FILE_WRITE_EA, part of FILE_GENERIC_WRITE and not a
	// planting right on its own.
	fileWriteEA uint32 = 0x0010
	// fileDeleteChild is FILE_DELETE_CHILD. It counts, because unlinking the
	// lock file and creating a new one is the same attack from the other side.
	fileDeleteChild uint32 = 0x0040
	// fileWriteAttributes is FILE_WRITE_ATTRIBUTES, part of
	// FILE_GENERIC_WRITE and not a planting right on its own.
	fileWriteAttributes uint32 = 0x0100
	// writeDAC is WRITE_DAC and writeOwner is WRITE_OWNER. Both count: an
	// account that can rewrite the ACL, or take ownership and then rewrite it,
	// can grant itself the rest — a right to become writable is a right to
	// write.
	writeDAC   uint32 = 0x00040000
	writeOwner uint32 = 0x00080000
	// standardRightsWrite is STANDARD_RIGHTS_WRITE, which is READ_CONTROL
	// alone and grants nothing here; it is named because FILE_GENERIC_WRITE
	// includes it.
	standardRightsWrite uint32 = 0x00020000
	// synchronize is SYNCHRONIZE, likewise part of FILE_GENERIC_WRITE.
	synchronize uint32 = 0x00100000
)

// Generic rights, which an ACE may carry INSTEAD of the specific rights they
// stand for (winnt.h). They are expanded before anything is compared — see
// [expandGeneric].
const (
	genericAll   uint32 = 0x10000000
	genericWrite uint32 = 0x40000000
)

// fileGenericWrite is the file object's GENERIC_MAPPING entry for
// GENERIC_WRITE (winnt.h, FILE_GENERIC_WRITE).
const fileGenericWrite uint32 = standardRightsWrite | fileAddFile | fileAddSubdirectory |
	fileWriteEA | fileWriteAttributes | synchronize

// fileAllAccess is what GENERIC_ALL maps to for a file object: every specific
// right this file names, plus the standard ones.
const fileAllAccess uint32 = fileGenericWrite | fileDeleteChild | writeDAC | writeOwner

// plantRights is every specific right that lets its holder put an entry into a
// directory, or take the ability to.
const plantRights uint32 = fileAddFile | fileAddSubdirectory | fileDeleteChild | writeDAC | writeOwner

// expandGeneric replaces the generic rights in an access mask with the
// specific rights they stand for.
//
// Windows maps generic bits through the object's GENERIC_MAPPING when an ACE
// is stored, so a mask read back from a DACL usually carries none — but it MAY,
// and comparing the two representations as unrelated bits gets deny
// subtraction wrong in both directions: a specific deny followed by a generic
// allow leaves the generic bit standing, and a generic deny never subtracts
// from a specific allow at all.
func expandGeneric(mask uint32) uint32 {
	expanded := mask
	//: GENERIC_WRITE stands for FILE_GENERIC_WRITE on a file object.
	if mask&genericWrite != 0 {
		expanded |= fileGenericWrite
	}
	//: GENERIC_ALL stands for everything, which here means every specific
	//: right this file knows how to name.
	if mask&genericAll != 0 {
		expanded |= fileAllAccess
	}
	//: the specific rights, with the generic bits themselves left in place —
	//: they are never compared against plantRights, which holds none.
	return expanded
}

// anyoneSids are the string-form security identifiers this rule reads as
// "anybody at all", which is what the Unix other-write bit means.
//
// S-1-1-0 is Everyone and S-1-5-11 is Authenticated Users — the two ADR 0081
// §D5 named when it described the check it was deferring, and the pair is kept
// exactly as named rather than widened here. S-1-5-32-545 (BUILTIN\Users) is
// the obvious third candidate and is deliberately NOT included: it is granted
// write on directories Windows ships, so adding it would refuse deployments
// this rule has no measurement about. That is recorded in ADR 0083 §Deferred
// rather than decided on a guess.
var anyoneSids = []string{sidEveryone, sidAuthenticatedUsers}

// sidEveryone is S-1-1-0, the identifier every account holds, authenticated or
// not.
const sidEveryone string = "S-1-1-0"

// sidAuthenticatedUsers is S-1-5-11, which every account that has logged in
// holds IN ADDITION to [sidEveryone] — see [tokenSet] for why that matters.
const sidAuthenticatedUsers string = "S-1-5-11"

// aclHeader mirrors the Win32 ACL structure (winnt.h). Eight bytes, and the
// ACEs follow it contiguously — which is why GetAce exists rather than a
// pointer walk here.
type aclHeader struct {
	AclRevision byte
	Sbz1        byte
	AclSize     uint16
	AceCount    uint16
	Sbz2        uint16
}

// aceEntry mirrors ACCESS_ALLOWED_ACE and ACCESS_DENIED_ACE (winnt.h), which
// have the identical layout and differ only in AceType. SidStart is the FIRST
// DWORD of a variable-length SID, so its ADDRESS is the SID and its value is
// meaningless on its own.
//
// It is valid for those two AceType values ONLY. An object ACE puts a flags
// word and up to two GUIDs where this struct puts the SID, so reading one
// through this type would hand ConvertSidToStringSidW bytes that are not a SID
// at all — which is why [walkDacl] checks the type BEFORE it reads the
// identifier and never the other way round.
type aceEntry struct {
	AceType  byte
	AceFlags byte
	AceSize  uint16
	Mask     uint32
	SidStart uint32
}

// dirWritableByAnyone reports whether any account can create an entry in dir,
// and renders what it found for the refusal's fields.
//
// A NULL DACL is the most permissive answer Windows has — it grants everyone
// full control — so it is reported as writable rather than as an absence.
// An EMPTY DACL is the opposite, granting nobody anything, and falls out of
// the loop as not writable.
func dirWritableByAnyone(dir string) (writable bool, observed string) {
	namep, convErr := syscall.UTF16PtrFromString(dir)
	//: a path with a NUL in it is not a path. Fail open: see the package
	//: comment on why a wrong refusal costs more than a wrong acceptance.
	if convErr != nil {
		//: no verdict.
		return false, ""
	}
	var dacl *aclHeader
	var descriptor syscall.Handle
	status, _, _ := procGetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(namep)),
		seFileObject,
		daclSecurityInformation,
		0, 0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)))
	//: the security information could not be read — the object may be on a
	//: filesystem with no ACL support at all, which is a fact about the
	//: medium and not about the directory. Fail open.
	if status != errorSuccess {
		//: no verdict, and the Win32 code travels so an operator can look it
		//: up rather than wonder whether the check ran.
		return false, "GetNamedSecurityInfoW=" + strconv.FormatUint(uint64(status), 10)
	}
	//: the descriptor owns the memory every ACE below points INTO, so it is
	//: released only after the walk.
	defer freeDescriptor(descriptor)
	//: a NULL DACL grants everyone full control. It is the most permissive
	//: state a Windows object can be in, and reading it as "no entries, so no
	//: grants" is the exact inversion that makes a security check useless.
	if dacl == nil {
		//: refused, and named so the reason is not mistaken for an ACE.
		return true, "null_dacl"
	}
	return walkDacl(dacl)
}

// walkDacl evaluates the discretionary list in order and reports the first
// grant of a planting right to an identifier meaning "anybody".
//
// Order matters and is honoured: a denied right is subtracted before a later
// allow is consulted, which is how Windows itself evaluates a list whose deny
// entries were placed ahead of its allow entries. Nothing here reorders them,
// because a list that is not in canonical order is evaluated in the order it
// is in, and guessing otherwise would be a different check.
func walkDacl(dacl *aclHeader) (writable bool, observed string) {
	//: two hypothetical accounts rather than two identifiers, because they are
	//: not independent: EVERY authenticated account holds Everyone AND
	//: Authenticated Users, so a deny addressed to Everyone constrains a later
	//: allow addressed to Authenticated Users. Keying denials by the ACE's
	//: literal SID misses that and refuses a directory nobody can write.
	tokens := newTokens()
	//: one pass per entry, in list order.
	for index := range uint32(dacl.AceCount) {
		ace, ok := aceAt(dacl, index)
		//: an entry that cannot be fetched ends the walk. Fail open rather
		//: than judge a list half-read.
		if !ok {
			//: no verdict.
			return false, ""
		}
		//: an entry this file does not know the LAYOUT of — an object ACE puts
		//: a flags word and up to two GUIDs where aceEntry puts the SID — is
		//: skipped before anything reads through it. The type check comes
		//: first for that reason and not for tidiness.
		if ace.AceType != accessAllowedAceType && ace.AceType != accessDeniedAceType {
			//: next entry.
			continue
		}
		//: an inherit-only entry grants nothing on this directory.
		if ace.AceFlags&inheritOnlyAce != 0 {
			//: next entry.
			continue
		}
		sid := sidOf(ace)
		//: an identifier naming a particular account or group is not the
		//: question — "anybody" is.
		if !anyone(sid) {
			//: next entry.
			continue
		}
		granted := tokens.apply(sid, ace.AceType, expandGeneric(ace.Mask))
		//: a grant that survives every preceding denial for at least one of
		//: the two accounts is the verdict.
		if granted != 0 {
			//: refused, naming who and what.
			return true, sid + "=0x" + strconv.FormatUint(uint64(granted), 16)
		}
	}
	//: nobody meaning "anybody" can put an entry here.
	return false, ""
}

// aceAt fetches one entry of the list.
func aceAt(dacl *aclHeader, index uint32) (ace *aceEntry, ok bool) {
	//: an unsafe.Pointer rather than a uintptr, because GetAce's out-parameter
	//: is LPVOID* and a uintptr round trip through a local is precisely the
	//: shape `go vet`'s unsafeptr check exists to refuse: between the store
	//: and the conversion the value is an integer the garbage collector does
	//: not follow.
	var raw unsafe.Pointer
	got, _, _ := procGetAce.Call(uintptr(unsafe.Pointer(dacl)), uintptr(index), uintptr(unsafe.Pointer(&raw)))
	//: GetAce returns a BOOL; zero is failure, and the pointer is untouched.
	if got == 0 || raw == nil {
		//: no entry.
		return nil, false
	}
	//: the pointer is INTO the security descriptor, which the caller still
	//: holds — nothing is copied and nothing is owned here.
	return (*aceEntry)(raw), true
}

// sidOf renders an entry's security identifier in its string form.
//
// The SID begins AT the address of SidStart and runs past it, so the field's
// value is meaningless and only its address is used. The string form is what
// the comparison needs, and asking for it also avoids binding EqualSid.
func sidOf(ace *aceEntry) string {
	rendered, renderErr := (*syscall.SID)(unsafe.Pointer(&ace.SidStart)).String()
	//: an identifier that cannot be rendered cannot be matched either, and an
	//: empty string matches nothing in anyoneSids.
	if renderErr != nil {
		//: no identifier.
		return ""
	}
	//: the canonical S-R-I-S-S… form.
	return rendered
}

// anyone reports whether sid is one of the identifiers that mean "anybody".
func anyone(sid string) bool {
	//: two entries, compared by byte equality on their canonical string form —
	//: which is exactly how the authz domain compares a resource, and for the
	//: same reason.
	for _, candidate := range anyoneSids {
		//: a match ends the search.
		if sid == candidate {
			//: anybody.
			return true
		}
	}
	//: a particular account or group.
	return false
}

// freeDescriptor releases the buffer GetNamedSecurityInfoW allocated.
func freeDescriptor(descriptor syscall.Handle) {
	//: LocalFree returns the handle on failure and NULL on success; there is
	//: nothing a caller could do about a refused free of a buffer it no longer
	//: references, and no error path here to carry it on.
	_, freeErr := syscall.LocalFree(descriptor)
	//: deliberately consulted and dropped, so the discard reads as a decision.
	if freeErr != nil {
		//: nothing to report, and nowhere to report it.
		return
	}
}
