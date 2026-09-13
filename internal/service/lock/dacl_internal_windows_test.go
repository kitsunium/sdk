//go:build windows

package lock

import (
	"encoding/binary"
	"syscall"
	"testing"
	"unsafe"
)

// This file is INTERNAL (package lock) because what it measures is the walk
// itself rather than the locker's public verdict, and because the shapes it
// drives cannot all be reached through the filesystem: a conditional entry
// needs an expression blob the kernel validates, and a malformed one is not
// something an ACL editor will write. It carries the same build constraint as
// dacl_windows.go, and the lane that executes it is the `windows` job of
// .github/workflows/e2e-cross.yml.
//
// The lists are assembled byte by byte and walked with the SHIPPED GetAce, so
// what runs here is the production reader over real Win32 structures — only
// the memory is this test's rather than the security descriptor's. The
// end-to-end proof that an object entry survives a round trip through NTFS
// lives in dacl_objectace_windows_test.go.

// aclRevisionDS is ACL_REVISION_DS, the highest revision GetAce accepts and
// the only one that may carry an object-type entry.
const aclRevisionDS byte = 4

// sidOfEveryone and the two others are the identifiers the rule reads as
// "anybody"; sidOfNobodyInParticular is a local account, which it does not.
const sidOfNobodyInParticular string = "S-1-5-21-1-2-3-1000"

// buildAce assembles one entry of a discretionary list.
//
// guids is how many of the two object GUIDs an object entry announces, and is
// ignored for the shapes that carry none.
func buildAce(t *testing.T, aceType, flags byte, sid string, mask uint32, guids int) []byte {
	t.Helper()
	converted, convErr := syscall.StringToSid(sid)
	//: a well-known identifier that will not convert is a broken runner.
	if convErr != nil {
		t.Fatalf("StringToSid(%s) = %v", sid, convErr)
	}
	raw := append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(converted)), syscall.GetLengthSid(converted))...)
	body := []byte(nil)
	shape, _ := shapeOf(aceType)
	//: an object entry puts a flags word and the GUIDs it announces between
	//: the mask and the identifier.
	if shape.object {
		announced := uint32(0)
		//: ACE_OBJECT_TYPE_PRESENT.
		if guids >= 1 {
			announced |= aceObjectTypePresent
		}
		//: ACE_INHERITED_OBJECT_TYPE_PRESENT.
		if guids >= 2 {
			announced |= aceInheritedObjectTypePresent
		}
		body = binary.LittleEndian.AppendUint32(body, announced)
		body = append(body, make([]byte, int(guidSize)*guids)...)
	}
	size := 8 + len(body) + len(raw)
	out := append([]byte(nil), aceType, flags)
	out = binary.LittleEndian.AppendUint16(out, uint16(size))
	out = binary.LittleEndian.AppendUint32(out, mask)
	out = append(out, body...)
	//: a callback entry trails a conditional expression AFTER the identifier,
	//: which is why the identifier stays where a plain entry keeps it. Four
	//: bytes of it are enough to prove the offset is unaffected.
	out = append(out, raw...)
	//: the trailing blob, present only where the shape says so.
	if shape.conditional {
		out = append(out, 'a', 'r', 't', 'x')
		binary.LittleEndian.PutUint16(out[2:4], uint16(len(out)))
	}
	return out
}

// buildAcl wraps entries in an ACL header and hands back a walkable list.
func buildAcl(entries ...[]byte) []byte {
	size := 8
	//: the header carries the TOTAL size, so the entries are measured first.
	for _, entry := range entries {
		size += len(entry)
	}
	out := append([]byte(nil), aclRevisionDS, 0)
	out = binary.LittleEndian.AppendUint16(out, uint16(size))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(entries)))
	out = binary.LittleEndian.AppendUint16(out, 0)
	//: the entries follow the header contiguously.
	for _, entry := range entries {
		out = append(out, entry...)
	}
	return out
}

// walk runs the production reader over a list this test assembled.
func walk(list []byte, onDirectory, onFilesWithin uint32) (granted bool, observed string) {
	return walkDacl((*aclHeader)(unsafe.Pointer(&list[0])), onDirectory, onFilesWithin)
}

// TestTheWalkReadsEveryDiscretionaryAceShape closes ADR 0084 §Deferred's third
// entry at the level of the layout.
//
// A DACL may carry eight entry types: the plain pair, the object pair that
// puts a flags word and up to two GUIDs before the identifier, and the two
// callback pairs that put a conditional expression after it. ADR 0084 decoded
// two of the eight and skipped the rest, which fails open — so a grant spelled
// in any of the other six was invisible.
//
// The GUID rows are the load-bearing ones: each announced GUID moves the
// identifier sixteen bytes, so a walk reading it at the plain offset would
// hand ConvertSidToStringSidW a flags word rather than a SID, and would then
// decide the entry names nobody.
func TestTheWalkReadsEveryDiscretionaryAceShape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// aceType is the discretionary entry type being driven.
		aceType byte
		// guids is how many object GUIDs the entry announces.
		guids int
	}
	tests := []tc{
		{"plain allow", accessAllowedAceType, 0},
		{"object allow, no GUID", accessAllowedObjectAceType, 0},
		{"object allow, an object type", accessAllowedObjectAceType, 1},
		{"object allow, both GUIDs", accessAllowedObjectAceType, 2},
		{"callback allow", accessAllowedCallbackAceType, 0},
		{"callback object allow, both GUIDs", accessAllowedCallbackObject, 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		list := buildAcl(buildAce(t, c.aceType, 0, sidEveryone, fileDeleteChild, c.guids))
		granted, observed := walk(list, replaceRights, contentRights)
		//: the grant is FILE_DELETE_CHILD to Everyone, which is the one right
		//: that hands a stranger a holder's inode.
		if !granted {
			t.Fatalf("a %s granting Everyone FILE_DELETE_CHILD was not seen (observed=%q)", c.name, observed)
		}
		//: and the diagnosis names the identifier the walk resolved, which is
		//: what proves the offset arithmetic rather than a lucky bit.
		if observed != sidEveryone+"=0x40" {
			t.Fatalf("a %s reported %q, want %q", c.name, observed, sidEveryone+"=0x40")
		}
	}
	//: one subtest per shape.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestADenialReachesEveryAccountHoldingTheIdentifierItNames pins ADR 0084
// §D3b's model against the third identifier ADR 0085 adds.
//
// The identifiers are nested: every authenticated account holds Everyone AND
// Authenticated Users, and every local interactive one holds BUILTIN\Users on
// top. A list that denies the outer identifier and then allows an inner one
// grants that right to nobody, and a walk keying denials on the literal SID
// reports it as granted and refuses a directory that is perfectly safe.
func TestADenialReachesEveryAccountHoldingTheIdentifierItNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// deniedTo is the identifier the leading deny names.
		deniedTo string
		// allowedTo is the identifier the following allow names.
		allowedTo string
		// granted says whether any modelled account is left holding the right.
		granted bool
	}
	tests := []tc{
		{"deny Everyone, allow Authenticated Users", sidEveryone, sidAuthenticatedUsers, false},
		{"deny Everyone, allow BUILTIN\\Users", sidEveryone, sidBuiltinUsers, false},
		{"deny BUILTIN\\Users, allow Everyone", sidBuiltinUsers, sidEveryone, true},
		{"deny a local account, allow Everyone", sidOfNobodyInParticular, sidEveryone, true},
		{"deny Everyone, allow Everyone", sidEveryone, sidEveryone, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		list := buildAcl(
			buildAce(t, accessDeniedAceType, 0, c.deniedTo, fileDeleteChild, 0),
			buildAce(t, accessAllowedAceType, 0, c.allowedTo, fileDeleteChild, 0))
		granted, observed := walk(list, replaceRights, contentRights)
		//: the third row is the one that must stay granted: an account outside
		//: BUILTIN\Users still holds Everyone, and the anonymous caller is
		//: exactly that account.
		if granted != c.granted {
			t.Fatalf("deny %s then allow %s = %v (observed=%q), want %v", c.deniedTo, c.allowedTo, granted, observed, c.granted)
		}
	}
	//: one subtest per row.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAConditionalEntryIsResolvedTowardsTheVerdictItCannotWeaken pins the one
// place the callback shapes need a policy rather than a layout.
//
// A callback entry carries an expression this package does not evaluate. Both
// dispositions are read in the direction that cannot turn a refusal into an
// acceptance: a conditional allow is read as granting, and a conditional deny
// is read as taking nothing away. Skipping both instead would make "add a
// condition" a way to put a grant where this rule cannot see it.
func TestAConditionalEntryIsResolvedTowardsTheVerdictItCannotWeaken(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// entries is the list, in the order the walk will read it.
		entries [][]byte
		// granted is the verdict.
		granted bool
	}
	conditionalAllow := func(t *testing.T) []byte {
		return buildAce(t, accessAllowedCallbackAceType, 0, sidEveryone, fileDeleteChild, 0)
	}
	conditionalDeny := func(t *testing.T) []byte {
		return buildAce(t, accessDeniedCallbackAceType, 0, sidEveryone, fileDeleteChild, 0)
	}
	plainAllow := func(t *testing.T) []byte {
		return buildAce(t, accessAllowedAceType, 0, sidEveryone, fileDeleteChild, 0)
	}
	tests := []tc{
		{"a conditional allow grants", [][]byte{conditionalAllow(t)}, true},
		{"a conditional deny takes nothing from a later allow", [][]byte{conditionalDeny(t), plainAllow(t)}, true},
		{"a conditional deny alone grants nothing either", [][]byte{conditionalDeny(t)}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		granted, observed := walk(buildAcl(c.entries...), replaceRights, contentRights)
		//: the verdict, and the diagnosis when it is the wrong one.
		if granted != c.granted {
			t.Fatalf("%s = %v (observed=%q), want %v", c.name, granted, observed, c.granted)
		}
	}
	//: one subtest per row.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestInheritanceDecidesWhichQuestionAnEntryAnswers is ADR 0085's second half,
// measured on the flags rather than on a filesystem.
//
// An entry applies to the directory itself unless it is INHERIT_ONLY, and it
// reaches the FILES created in the directory when it carries
// OBJECT_INHERIT_ACE. Those are two different questions with two different
// masks, and ADR 0084 asked only the first — so a directory whose files all
// inherit a write nobody holds on the directory was accepted, which hands
// every account the fencing ledger.
func TestInheritanceDecidesWhichQuestionAnEntryAnswers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// flags is the entry's ACE_HEADER flags word.
		flags byte
		// mask is what the entry grants.
		mask uint32
		// granted is checkDir's verdict for that entry.
		granted bool
	}
	tests := []tc{
		{"applies here, and may unlink somebody else's entry", 0, fileDeleteChild, true},
		{"applies here, and may only create one", 0, fileAddFile | fileAddSubdirectory, false},
		{"inherit-only, and reaches no file", inheritOnlyAce, fileGenericWrite, false},
		{"inherit-only, and every file created here inherits a write", inheritOnlyAce | objectInheritAce, fileGenericWrite, true},
		{"applies here and to every file, read-only", objectInheritAce, standardRightsWrite, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		list := buildAcl(buildAce(t, accessAllowedAceType, c.flags, sidEveryone, c.mask, 0))
		granted, observed := walk(list, replaceRights, contentRights)
		//: the verdict checkDir would reach on that one entry.
		if granted != c.granted {
			t.Fatalf("an entry with flags 0x%02x and mask 0x%08x = %v (observed=%q), want %v", c.flags, c.mask, granted, observed, c.granted)
		}
	}
	//: one subtest per row.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAnEntryWithNoRoomForItsIdentifierIsNotRead pins the bound the object
// shapes make necessary.
//
// The identifier's offset is computed from the entry's own flags word, so a
// malformed entry can put it past the end of the entry — and reading there
// would hand ConvertSidToStringSidW the bytes of whatever follows in the list.
// The size is checked before the pointer is formed.
func TestAnEntryWithNoRoomForItsIdentifierIsNotRead(t *testing.T) {
	t.Parallel()
	entry := buildAce(t, accessAllowedObjectAceType, 0, sidEveryone, fileDeleteChild, 0)
	//: the entry announces both GUIDs it does not carry, which pushes the
	//: identifier thirty-two bytes past where its bytes actually are.
	binary.LittleEndian.PutUint32(entry[8:12], aceObjectTypePresent|aceInheritedObjectTypePresent)
	granted, observed := walk(buildAcl(entry), replaceRights, contentRights)
	//: not granted, whichever way it is refused — the bound check declines to
	//: read, or GetAce declines to hand the entry over at all.
	if granted {
		t.Fatalf("an entry announcing GUIDs it does not carry was read as a grant (observed=%q)", observed)
	}
}
