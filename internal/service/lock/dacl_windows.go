//go:build windows

// Package lock — "who could interfere with the lock file in this directory?",
// asked of Windows in the only vocabulary Windows has for it.
//
// # Why the POSIX question has no answer here, and this one does
//
// checkDir and checkChain both rest on a question about a directory. On Unix
// each is a single mode bit. On Windows os.Stat has no permission bits to read
// — it SYNTHESISES a mode from FILE_ATTRIBUTE_READONLY, so every writable
// directory reports 0777 and every read-only one reports 0555 — so the POSIX
// rule would refuse every directory a caller could name, which is ADR 0018
// §(a)'s failure mode wearing an error that blames the deployment (ADR 0081
// §D5).
//
// The questions themselves are perfectly answerable on this platform; they are
// just not a mode. They are the directory's discretionary access control list.
//
// # They are two questions, and Windows answers them with different bits
//
// ADR 0084 asked ONE question here — "is there an entry granting a planting
// right to an identifier meaning anybody?" — and gave both rules the same
// mask. The Unix side has never done that: checkDir accepts 0777|sticky and
// checkChain's plantable refuses it, because sticky governs UNLINKING an entry
// that exists while planting a component CREATES one at a free name.
//
// Windows draws that same line, and draws it finer, because create and delete
// are separate bits rather than one sticky flag (ADR 0086):
//
//   - [replaceRights] — what lets a stranger take away the entry a holder
//     created. That is checkDir's question.
//   - [createRights] — what lets a stranger put a directory at a name nobody
//     has taken. That is plantable's question.
//   - [contentRights] — what the FILES created in the directory inherit. It
//     has no Unix counterpart at all, because a lock file there is created
//     0600 whatever the directory's mode says, while on Windows it inherits
//     the directory's list. A directory nobody can unlink from, whose files
//     every account may write, hands a stranger the fencing ledger.
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
// fetched — answers "no, nobody outside the holder can interfere". A wrong
// REFUSAL here costs a caller a locker that will never build on a directory
// that is perfectly safe; a wrong acceptance costs the hardening this file
// adds and leaves the platform exactly where it was before. Those are not
// symmetric, and the asymmetry decides.
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
// are not part of this question, and the SACL cannot GRANT anything at all
// (ADR 0086 §D5).
const daclSecurityInformation uintptr = 0x00000004

// errorSuccess is ERROR_SUCCESS, the value GetNamedSecurityInfoW returns when
// it succeeded.
const errorSuccess uintptr = 0

// The discretionary ACE types (winnt.h). Every type a DACL may carry is here:
// the plain pair, the object pair that puts GUIDs before the identifier, and
// the callback pairs that put a conditional expression after it. The system
// types — audit, alarm, mandatory label, scoped policy, access filter — live
// in the SACL and have no representation in a discretionary list.
const (
	accessAllowedAceType         byte = 0x00
	accessDeniedAceType          byte = 0x01
	accessAllowedObjectAceType   byte = 0x05
	accessDeniedObjectAceType    byte = 0x06
	accessAllowedCallbackAceType byte = 0x09
	accessDeniedCallbackAceType  byte = 0x0A
	accessAllowedCallbackObject  byte = 0x0B
	accessDeniedCallbackObject   byte = 0x0C
)

// ACE header flags (winnt.h) this rule reads.
const (
	// objectInheritAce is OBJECT_INHERIT_ACE: the entry is inherited by the
	// FILES created in this directory, which is what [contentRights] is asked
	// about.
	objectInheritAce byte = 0x01
	// inheritOnlyAce is INHERIT_ONLY_ACE: the entry describes what children
	// inherit and grants nothing on the object itself.
	inheritOnlyAce byte = 0x08
)

// Object-ACE flags (winnt.h): which of the two GUIDs the entry carries between
// its mask and its identifier. Each present GUID pushes the SID sixteen bytes
// further along, which is the whole reason an object entry cannot be read
// through a fixed struct.
const (
	aceObjectTypePresent          uint32 = 0x00000001
	aceInheritedObjectTypePresent uint32 = 0x00000002
)

// Specific rights (winnt.h). The first two carry two names apiece, and the
// duality is the point: bit 0x0002 is FILE_ADD_FILE on a DIRECTORY and
// FILE_WRITE_DATA on a FILE, and this rule asks about it in both senses — once
// of the lock directory, once of the lock files that inherit the same entry.
const (
	// fileAddFile is FILE_ADD_FILE on a directory, FILE_WRITE_DATA on a file.
	fileAddFile uint32 = 0x0002
	// fileAddSubdirectory is FILE_ADD_SUBDIRECTORY on a directory,
	// FILE_APPEND_DATA on a file.
	fileAddSubdirectory uint32 = 0x0004
	// fileWriteEA is FILE_WRITE_EA, part of FILE_GENERIC_WRITE and neither a
	// planting right nor a way to alter a file's contents.
	fileWriteEA uint32 = 0x0010
	// fileDeleteChild is FILE_DELETE_CHILD: unlink an entry of this directory
	// WITHOUT owning it. It is the one right that turns a lock file into
	// somebody else's inode.
	fileDeleteChild uint32 = 0x0040
	// fileWriteAttributes is FILE_WRITE_ATTRIBUTES, likewise part of
	// FILE_GENERIC_WRITE and likewise harmless here.
	fileWriteAttributes uint32 = 0x0100
	// deleteObject is DELETE, the standard right that removes the object it is
	// held on — a file, here, rather than an entry of a directory.
	deleteObject uint32 = 0x00010000
	// standardRightsWrite is STANDARD_RIGHTS_WRITE, which is READ_CONTROL
	// alone and grants nothing here; it is named because FILE_GENERIC_WRITE
	// includes it.
	standardRightsWrite uint32 = 0x00020000
	// writeDAC is WRITE_DAC and writeOwner is WRITE_OWNER. Both count, in
	// every mask: an account that can rewrite the list, or take ownership and
	// then rewrite it, can grant itself the rest — a right to become writable
	// is a right to write.
	writeDAC   uint32 = 0x00040000
	writeOwner uint32 = 0x00080000
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
const fileAllAccess uint32 = fileGenericWrite | fileDeleteChild | deleteObject | writeDAC | writeOwner

// replaceRights is what lets its holder take away an entry it did not create —
// [checkDir]'s question, and the exact Windows spelling of the Unix rule that
// refuses 0777 and accepts 0777|sticky.
//
// FILE_ADD_FILE is deliberately NOT in it. Creating an entry at a free name is
// what the sticky bit permits and what ADR 0052's table has accepted since it
// was written; what it costs is a lock file planted before any holder exists,
// which is a denial of service and is recorded as such rather than smuggled in
// here (ADR 0081 §Deferred, ADR 0086 §D6).
const replaceRights uint32 = fileDeleteChild | writeDAC | writeOwner

// createRights is what lets its holder put a directory at a name nobody has
// taken — [plantable]'s question, and the reason it and [checkDir] disagree
// about the same identifier on %ProgramData%.
//
// A path component is always a directory: a junction and a Windows directory
// symbolic link are both created with FILE_ADD_SUBDIRECTORY, and a file cannot
// be a component of a longer path.
const createRights uint32 = fileAddSubdirectory | writeDAC | writeOwner

// contentRights is what lets its holder alter or remove the lock FILE itself,
// read off the entries that the files created in the directory inherit.
//
// This half has no Unix counterpart. There a lock file is created 0600 no
// matter what the directory's mode says, so a world-writable directory never
// makes the lock file world-writable. On Windows the mode passed to os.OpenFile
// is meaningless and the new file takes the directory's inheritable entries
// instead — so a directory granting Everyone an inherited write hands every
// account the fencing ledger, which LockFileEx protects only while somebody is
// holding it.
const contentRights uint32 = fileAddFile | fileAddSubdirectory | deleteObject | writeDAC | writeOwner

// Offsets, from the start of an ACE, at which the variable-length identifier
// begins (winnt.h). They are the two shapes a discretionary entry comes in.
const (
	// plainSidOffset is where ACCESS_ALLOWED_ACE and the callback variant put
	// it: straight after the 4-byte header and the 4-byte mask.
	plainSidOffset uintptr = 8
	// objectSidOffset is where the object variants put it when they announce
	// no GUID: after the header, the mask and the flags word.
	objectSidOffset uintptr = 12
	// guidSize is how far each announced GUID moves it.
	guidSize uintptr = 16
	// smallestSid is the length of the shortest legal SID — one revision
	// byte, one sub-authority count and a six-byte authority. An entry with no
	// room for even that is not one to read an identifier out of.
	smallestSid uintptr = 8
	// subAuthorityWidth is how much each sub-authority adds to that, and the
	// COUNT of them is a byte inside the identifier — so the length of a SID
	// is data read from the same entry whose size has to contain it.
	subAuthorityWidth uintptr = 4
)

// anyoneSids are the string-form security identifiers this rule reads as
// "anybody at all", which is what the Unix other-write bit means.
//
// S-1-1-0 is Everyone and S-1-5-11 is Authenticated Users — the two ADR 0081
// §D5 named. S-1-5-32-545 (BUILTIN\Users) is the third, and it was excluded
// until ADR 0086: every local interactive account is in that group and on a
// domain-joined machine so is Domain Users, so a directory granting it a right
// IS one this rule's own sentence describes. What kept it out was that
// including it refused %ProgramData%, which was true only while both rules
// shared one mask — %ProgramData% grants the group FILE_ADD_FILE and
// FILE_ADD_SUBDIRECTORY and not FILE_DELETE_CHILD, which is create-but-not-
// replace, which is what 0777|sticky means.
var anyoneSids = []string{sidEveryone, sidAuthenticatedUsers, sidBuiltinUsers}

// sidEveryone is S-1-1-0, the identifier every account holds, authenticated or
// not.
const sidEveryone string = "S-1-1-0"

// sidAuthenticatedUsers is S-1-5-11, which every account that has logged in
// holds IN ADDITION to [sidEveryone] — see [tokenSet] for why that matters.
const sidAuthenticatedUsers string = "S-1-5-11"

// sidBuiltinUsers is S-1-5-32-545, BUILTIN\Users — the group every local
// interactive account belongs to, and on a domain-joined machine one that
// contains Domain Users as well.
const sidBuiltinUsers string = "S-1-5-32-545"

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

// aceEntry mirrors the prefix EVERY discretionary ACE shares (winnt.h): the
// four-byte ACE_HEADER and the access mask.
//
// Body is the first word after the mask, and what it means depends on the
// type. On a plain or callback entry it is the first word of the SID, so only
// its ADDRESS matters; on an object entry it is the flags word saying which
// GUIDs precede the SID. [sidOf] is the one place that difference is resolved,
// and it resolves it from the TYPE before it reads through the pointer — which
// is a memory-safety ordering and not a tidiness one.
type aceEntry struct {
	AceType  byte
	AceFlags byte
	AceSize  uint16
	Mask     uint32
	Body     uint32
}

// aceShape is what this file knows about one ACE type.
type aceShape struct {
	// allow says the entry grants rather than denies.
	allow bool
	// object says the identifier sits behind a flags word and up to two GUIDs.
	object bool
	// conditional says the entry carries an expression this file does not
	// evaluate — see [tokenSet.apply] for which way that is resolved.
	conditional bool
}

// shapeOf reports the layout and disposition of an ACE type, and whether this
// file knows it at all.
func shapeOf(aceType byte) (shape aceShape, known bool) {
	switch aceType {
	//: the pair every ordinary ACL is made of.
	case accessAllowedAceType:
		return aceShape{allow: true}, true
	//: its denying twin, identical in layout.
	case accessDeniedAceType:
		return aceShape{}, true
	//: the object pair, which an NTFS directory does store — measured, and the
	//: reason ADR 0084 §Deferred's premise did not hold.
	case accessAllowedObjectAceType:
		return aceShape{allow: true, object: true}, true
	//: its denying twin.
	case accessDeniedObjectAceType:
		return aceShape{object: true}, true
	//: the callback pair, which puts a conditional expression AFTER the
	//: identifier and therefore leaves the identifier where a plain entry
	//: keeps it.
	case accessAllowedCallbackAceType:
		return aceShape{allow: true, conditional: true}, true
	//: its denying twin.
	case accessDeniedCallbackAceType:
		return aceShape{conditional: true}, true
	//: both variations at once.
	case accessAllowedCallbackObject:
		return aceShape{allow: true, object: true, conditional: true}, true
	//: and its denying twin.
	case accessDeniedCallbackObject:
		return aceShape{object: true, conditional: true}, true
	//: an entry this file does not know the layout of. It is skipped before
	//: anything reads through it, which fails open.
	default:
		return aceShape{}, false
	}
}

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
	//: they are never compared against a rights mask, which holds none.
	return expanded
}

// dirGrantsAnyone reports whether any account can interfere with the lock file
// in dir, and renders what it found for the refusal's fields.
//
// onDirectory is the rights that matter when an entry applies to dir itself;
// onFilesWithin is the rights that matter when an entry is inherited by the
// files created in it, and is zero for a caller that is not asking about them.
//
// A NULL DACL is the most permissive answer Windows has — it grants everyone
// full control — so it is reported as writable rather than as an absence.
// An EMPTY DACL is the opposite, granting nobody anything, and falls out of
// the loop as not writable.
func dirGrantsAnyone(dir string, onDirectory, onFilesWithin uint32) (granted bool, observed string) {
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
	return walkDacl(dacl, onDirectory, onFilesWithin)
}

// walkDacl evaluates the discretionary list in order and reports the first
// grant of a wanted right to an identifier meaning "anybody".
//
// Order matters and is honoured: a denied right is subtracted before a later
// allow is consulted, which is how Windows itself evaluates a list whose deny
// entries were placed ahead of its allow entries. Nothing here reorders them,
// because a list that is not in canonical order is evaluated in the order it
// is in, and guessing otherwise would be a different check.
func walkDacl(dacl *aclHeader, onDirectory, onFilesWithin uint32) (granted bool, observed string) {
	//: hypothetical accounts rather than identifiers, because they are not
	//: independent: EVERY authenticated account holds Everyone AND
	//: Authenticated Users, so a deny addressed to Everyone constrains a later
	//: allow addressed to Authenticated Users. Keying denials by the ACE's
	//: literal SID misses that and refuses a directory nobody can write.
	//: TWO sets, because this walk asks two questions of one list and a deny
	//: reaches only the object its own flags describe — see [reachOf].
	directoryTokens, fileTokens := newTokens(), newTokens()
	//: one pass per entry, in list order.
	for index := range uint32(dacl.AceCount) {
		ace, ok := aceAt(dacl, index)
		//: an entry that cannot be fetched ends the walk. Fail open rather
		//: than judge a list half-read.
		if !ok {
			//: no verdict.
			return false, ""
		}
		shape, known := shapeOf(ace.AceType)
		//: an entry this file does not know the LAYOUT of is skipped before
		//: anything reads through it. The type check comes first for that
		//: reason and not for tidiness.
		if !known {
			//: next entry.
			continue
		}
		onDir, onFiles := reachOf(ace.AceFlags)
		//: an entry reaching neither object decides nothing. No such flags
		//: word exists today — inherit-only without object inheritance still
		//: reaches subdirectories — but the walk does not depend on that.
		if !onDir && !onFiles {
			//: next entry.
			continue
		}
		sid := sidOf(ace, shape)
		//: an identifier naming a particular account or group is not the
		//: question — "anybody" is.
		if !anyone(sid) {
			//: next entry.
			continue
		}
		left := fold(sid, shape, expandGeneric(ace.Mask), reach{onDir, onFiles},
			pair{directoryTokens, onDirectory}, pair{fileTokens, onFilesWithin})
		//: a grant that survives every preceding denial for at least one
		//: account on at least one object is the verdict.
		if left != 0 {
			//: refused, naming who and what.
			return true, sid + "=0x" + strconv.FormatUint(uint64(left), 16)
		}
	}
	//: nobody meaning "anybody" can interfere here.
	return false, ""
}

// reach says which objects one entry's inheritance flags reach.
type reach struct {
	// directory is the lock directory itself.
	directory bool
	// files is every file created in it, which inherits the entry.
	files bool
}

// pair binds one object's accounts to the rights that object is asked about.
type pair struct {
	// tokens is that object's denial state, kept apart from the other's.
	tokens *tokenSet
	// rights is the mask the caller wants to know about for that object, and
	// is zero for a caller not asking about it at all.
	rights uint32
}

// reachOf reduces an entry's inheritance flags to the objects it governs.
//
// An entry applies to the directory itself unless it is inherit-only, and it
// reaches the files created in the directory when it carries
// OBJECT_INHERIT_ACE. Both can be true of one entry, and then it is folded
// into both states — which is not the same as folding it into one shared
// state, because an entry reaching only ONE of them must not constrain the
// other.
func reachOf(flags byte) (directory, files bool) {
	//: an inherit-only entry describes children and grants nothing here.
	directory = flags&inheritOnlyAce == 0
	//: OBJECT_INHERIT_ACE is what the lock files created in this directory
	//: will carry — a different question with a different mask.
	files = flags&objectInheritAce != 0
	//: the objects this entry decides anything about.
	return directory, files
}

// fold applies one entry to each object it reaches, and reports what it leaves
// granted on any of them.
//
// The two states never see each other's entries. A deny that applies to the
// directory alone is not a deny on the files created in it, and treating it as
// one accepted a directory whose every lock file inherited the right to
// rewrite its own list.
func fold(sid string, shape aceShape, mask uint32, reached reach, onDirectory, onFiles pair) (granted uint32) {
	//: the directory's own state, if this entry applies to it.
	if reached.directory {
		granted |= onDirectory.tokens.apply(sid, shape, mask, onDirectory.rights)
	}
	//: the state of the files the directory will create, if this entry is
	//: inherited by them. A caller not asking about them passes a zero mask,
	//: which grants nothing whatever the list says.
	if reached.files {
		granted |= onFiles.tokens.apply(sid, shape, mask, onFiles.rights)
	}
	//: what at least one account holds on at least one object.
	return granted
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
// Where that identifier BEGINS is what the two ACE shapes disagree about: a
// plain or callback entry puts it straight after the mask, an object entry
// puts a flags word and up to two GUIDs in front of it. Reading the second
// through the first's offset would hand ConvertSidToStringSidW a flags word
// and half a GUID, so the shape decides the offset before anything is read.
func sidOf(ace *aceEntry, shape aceShape) string {
	offset := plainSidOffset
	//: an object entry announces its GUIDs in the word a plain entry uses for
	//: the identifier's first bytes, and each announced GUID moves the
	//: identifier sixteen bytes further along.
	if shape.object {
		offset = objectSidOffset + guidSize*uintptr(guidsIn(ace.Body))
	}
	//: an entry too small to hold even the shortest legal SID at that offset
	//: is malformed, and reading it would run past the ACE into the next one.
	if offset+smallestSid > uintptr(ace.AceSize) {
		//: no identifier, and an empty string matches nothing in anyoneSids.
		return ""
	}
	//: the identifier's own LENGTH is data as well — one byte of it says how
	//: many sub-authorities follow — so the whole SID has to fit too, or
	//: ConvertSidToStringSidW would render bytes belonging to the next entry
	//: and could name an identifier this rule reads as "anybody".
	subAuthorities := uintptr(*(*byte)(unsafe.Add(unsafe.Pointer(ace), offset+1)))
	if offset+smallestSid+subAuthorityWidth*subAuthorities > uintptr(ace.AceSize) {
		//: no identifier.
		return ""
	}
	rendered, renderErr := (*syscall.SID)(unsafe.Add(unsafe.Pointer(ace), offset)).String()
	//: an identifier that cannot be rendered cannot be matched either.
	if renderErr != nil {
		//: no identifier.
		return ""
	}
	//: the canonical S-R-I-S-S… form.
	return rendered
}

// guidsIn counts the object GUIDs an object entry's flags word announces.
func guidsIn(flags uint32) (count int) {
	//: ACE_OBJECT_TYPE_PRESENT, the first of the two.
	if flags&aceObjectTypePresent != 0 {
		count++
	}
	//: ACE_INHERITED_OBJECT_TYPE_PRESENT, which winnt.h declares second and
	//: which therefore follows the first in the entry's bytes.
	if flags&aceInheritedObjectTypePresent != 0 {
		count++
	}
	//: zero, one or two.
	return count
}

// anyone reports whether sid is one of the identifiers that mean "anybody".
func anyone(sid string) bool {
	//: three entries, compared by byte equality on their canonical string form
	//: — which is exactly how the authz domain compares a resource, and for
	//: the same reason.
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
