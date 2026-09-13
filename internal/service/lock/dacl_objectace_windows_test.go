//go:build windows

package lock_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file drives an ACE shape `icacls` cannot spell. It builds the access
// control list byte by byte and hands it to SetNamedSecurityInfoW, which is
// bound here and NOWHERE in production — the check reads a DACL, it never
// writes one, so the export belongs to the test that needs it.
//
// What it measures is whether an ordinary NTFS directory will STORE an
// object-type entry, which is the premise ADR 0084 §Deferred rested on when it
// skipped them ("they do not appear on ordinary filesystem objects"). Measured
// on windows-latest: revision 2 is refused with ERROR_INVALID_ACL (1336) and
// revision 4 is accepted, stored, and read back as type 0x05.

// aclRevisionDS is ACL_REVISION_DS (winnt.h), the only revision that may carry
// an object-type entry.
const aclRevisionDS byte = 4

// Object ACE types (winnt.h) and the flags that say which of the two GUIDs a
// given entry carries between its mask and its SID.
const (
	accessAllowedObjectAceType byte   = 0x05
	aceObjectTypePresent       uint32 = 0x00000001
	aceInheritedObjectTypeSet  uint32 = 0x00000002
)

// fileDeleteChildRight is FILE_DELETE_CHILD — the one right that lets its
// holder unlink an entry it does not own, which is what `checkDir` refuses.
const fileDeleteChildRight uint32 = 0x00000040

// fileAllAccessRight is FILE_ALL_ACCESS, granted to the local Administrators
// group so t.TempDir's cleanup can still remove a directory this test gave a
// protected access control list.
const fileAllAccessRight uint32 = 0x001F01FF

// sidAdministrators is S-1-5-32-544, BUILTIN\Administrators.
const sidAdministrators string = "S-1-5-32-544"

// procSetNamedSecurityInfo writes a named object's security descriptor. It
// returns a Win32 error code as its RESULT, exactly as its reading twin does.
var procSetNamedSecurityInfo = syscall.NewLazyDLL("advapi32.dll").NewProc("SetNamedSecurityInfoW")

// sidBytes renders a string-form identifier as the raw bytes an ACE carries.
func sidBytes(t *testing.T, sid string) []byte {
	t.Helper()
	converted, convErr := syscall.StringToSid(sid)
	//: a well-known identifier that will not convert is a broken runner, not
	//: a property of the code under test.
	if convErr != nil {
		t.Fatalf("StringToSid(%s) = %v", sid, convErr)
	}
	length := syscall.GetLengthSid(converted)
	//: the SID is variable-length and lives where the conversion put it, so it
	//: is copied out before anything else runs.
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(converted)), length)...)
}

// ace assembles one access-allowed entry.
//
// guids is how many of the two object GUIDs the entry carries, which is the
// whole reason an object ACE needs decoding rather than reading through a
// fixed struct: each one pushes the SID sixteen bytes further along.
func ace(t *testing.T, aceType byte, sid string, mask uint32, guids int) []byte {
	t.Helper()
	raw := sidBytes(t, sid)
	body := []byte{}
	//: an object entry puts a flags word, then the GUIDs it announces, before
	//: the identifier; a plain one puts the identifier straight after the mask.
	if aceType == accessAllowedObjectAceType {
		flags := uint32(0)
		//: one GUID is the object type, two adds the inherited object type —
		//: the order winnt.h declares them in.
		if guids >= 1 {
			flags |= aceObjectTypePresent
		}
		//: the second GUID.
		if guids >= 2 {
			flags |= aceInheritedObjectTypeSet
		}
		body = binary.LittleEndian.AppendUint32(body, flags)
		body = append(body, make([]byte, 16*guids)...)
	}
	size := 8 + len(body) + len(raw)
	out := make([]byte, 0, size)
	out = append(out, aceType, 0)
	out = binary.LittleEndian.AppendUint16(out, uint16(size))
	out = binary.LittleEndian.AppendUint32(out, mask)
	out = append(out, body...)
	//: the identifier closes the entry, and its length is what makes the whole
	//: structure variable-sized.
	return append(out, raw...)
}

// acl wraps entries in an ACL header of the given revision.
func acl(revision byte, entries ...[]byte) []byte {
	size := 8
	//: the header carries the TOTAL size, so the entries are measured first.
	for _, entry := range entries {
		size += len(entry)
	}
	out := make([]byte, 0, size)
	out = append(out, revision, 0)
	out = binary.LittleEndian.AppendUint16(out, uint16(size))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(entries)))
	out = binary.LittleEndian.AppendUint16(out, 0)
	//: the entries follow the header contiguously, which is why GetAce exists
	//: rather than a pointer walk.
	for _, entry := range entries {
		out = append(out, entry...)
	}
	return out
}

// applyProtectedDacl installs list as dir's discretionary list, detached from
// inheritance, and FAILS rather than skips when the kernel refuses it.
func applyProtectedDacl(t *testing.T, dir string, list []byte) {
	t.Helper()
	namep, convErr := syscall.UTF16PtrFromString(dir)
	//: a path that will not convert is a broken test, not a verdict.
	if convErr != nil {
		t.Fatalf("UTF16PtrFromString(%s) = %v", dir, convErr)
	}
	// SE_FILE_OBJECT, DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION.
	const seFileObject uintptr = 1
	const daclInfo uintptr = 0x00000004 | 0x80000000
	status, _, _ := procSetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(namep)), seFileObject, daclInfo,
		0, 0, uintptr(unsafe.Pointer(&list[0])), 0)
	//: ERROR_SUCCESS is zero; anything else means the list never reached the
	//: filesystem and the row below would assert nothing.
	if status != 0 {
		t.Fatalf("SetNamedSecurityInfoW on %s = %d (%v) — the list never reached the filesystem, so this row asserts nothing and is a failure rather than a skip", dir, status, syscall.Errno(status))
	}
}

// TestAnObjectTypeAceIsJudgedRatherThanSkipped closes ADR 0084 §Deferred's
// third entry, and it closes it because the premise turned out to be wrong.
//
// That record skipped ACCESS_ALLOWED_OBJECT_ACE and its denied twin on the
// ground that "they are an Active Directory mechanism, and they do not appear
// on ordinary filesystem objects" — an entry whose layout the walk did not
// know was one it did not judge, which fails open.
//
// An ordinary NTFS directory stores one. Measured here rather than argued:
// SetNamedSecurityInfoW accepts a revision-4 list carrying an object entry,
// and GetNamedSecurityInfoW reads it straight back. So a directory granting
// Everyone the right to unlink somebody else's lock file was ACCEPTED, because
// the grant was spelled in an ACE shape the walk stepped over.
//
// The three rows differ only in how many GUIDs the entry carries between its
// mask and its SID, because that is the entire difficulty: each one moves the
// identifier sixteen bytes, and a walk that read it at the fixed offset would
// hand ConvertSidToStringSidW bytes that are not a SID at all.
func TestAnObjectTypeAceIsJudgedRatherThanSkipped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// guids is how many of the two object GUIDs the entry announces.
		guids int
	}
	tests := []tc{
		{"no GUID", 0},
		{"an object type", 1},
		{"an object type and an inherited object type", 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		locks := filepath.Join(t.TempDir(), "locks")
		//: a child of t.TempDir(), so the temporary directory's own cleanup
		//: never has to remove the one carrying a protected list.
		if err := os.Mkdir(locks, 0o700); err != nil {
			t.Fatalf("creating the lock directory = %v", err)
		}
		//: the Administrators entry is what keeps the directory removable
		//: after the list is detached from inheritance; it grants no identity
		//: this rule reads as "anybody".
		applyProtectedDacl(t, locks, acl(aclRevisionDS,
			ace(t, accessAllowedObjectAceType, "S-1-1-0", fileDeleteChildRight, c.guids),
			ace(t, 0x00, sidAdministrators, fileAllAccessRight, 0)))
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: locks})
		//: accepted here is the gap: FILE_DELETE_CHILD to Everyone is the one
		//: right that hands a stranger the holder's inode.
		if locker != nil {
			t.Fatalf("a directory granting Everyone FILE_DELETE_CHILD through an object-type ACE carrying %d GUID(s) was accepted", c.guids)
		}
		//: the code, because the remedy is an ACL and not a retry.
		if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
			t.Fatalf("NewFileLocker on a directory whose object-type ACE carries %d GUID(s) = %v, want LOCK_DIRECTORY_UNSAFE", c.guids, err)
		}
	}
	//: one subtest per row, each on its own directory.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
