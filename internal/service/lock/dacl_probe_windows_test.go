//go:build windows

package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// TEMPORARY PROBE — deleted before this branch merges. It exists to read four
// facts off a real windows-latest kernel that cannot be derived locally, and
// it ends in t.Fatalf on purpose: e2e-cross runs `go test` without -v, so a
// passing package's output is discarded and the only way to READ a measurement
// is to fail.

const (
	probeSidEveryone     = "S-1-1-0"
	probeSidAuthUsers    = "S-1-5-11"
	probeSidBuiltinUsers = "S-1-5-32-545"
)

var (
	probeProcSetNamedSecurityInfo = modadvapi32.NewProc("SetNamedSecurityInfoW")
	probeProcLookupPrivilegeName  = modadvapi32.NewProc("LookupPrivilegeNameW")
	probeProcLookupPrivilegeValue = modadvapi32.NewProc("LookupPrivilegeValueW")
	probeProcAdjustTokenPriv      = modadvapi32.NewProc("AdjustTokenPrivileges")
)

func probeDumpDacl(t *testing.T, dir string) {
	t.Helper()
	namep, convErr := syscall.UTF16PtrFromString(dir)
	if convErr != nil {
		t.Logf("  [%s] UTF16 conversion failed: %v", dir, convErr)
		return
	}
	var dacl *aclHeader
	var descriptor syscall.Handle
	status, _, _ := procGetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(namep)), seFileObject, daclSecurityInformation,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&descriptor)))
	if status != errorSuccess {
		t.Logf("  [%s] GetNamedSecurityInfoW=%d", dir, status)
		return
	}
	defer freeDescriptor(descriptor)
	if dacl == nil {
		t.Logf("  [%s] NULL DACL", dir)
		return
	}
	t.Logf("  [%s] rev=%d aceCount=%d size=%d", dir, dacl.AclRevision, dacl.AceCount, dacl.AclSize)
	for index := range uint32(dacl.AceCount) {
		ace, ok := aceAt(dacl, index)
		if !ok {
			t.Logf("    #%d GetAce failed", index)
			continue
		}
		sid := "<unreadable-or-not-a-plain-ace>"
		if ace.AceType == accessAllowedAceType || ace.AceType == accessDeniedAceType {
			sid = sidOf(ace)
		}
		t.Logf("    #%d type=0x%02x flags=0x%02x size=%d mask=0x%08x sid=%s", index, ace.AceType, ace.AceFlags, ace.AceSize, ace.Mask, sid)
	}
	base, baseWhy := dirWritableByAnyone(dir)
	orig := anyoneSids
	anyoneSids = []string{probeSidEveryone, probeSidAuthUsers, probeSidBuiltinUsers}
	wide, wideWhy := dirWritableByAnyone(dir)
	anyoneSids = orig
	t.Logf("    VERDICT shipped=%v(%q) withUsers=%v(%q)", base, baseWhy, wide, wideWhy)
}

func probeTokenPrivileges(t *testing.T) {
	t.Helper()
	token, openErr := syscall.OpenCurrentProcessToken()
	if openErr != nil {
		t.Logf("  OpenCurrentProcessToken = %v", openErr)
		return
	}
	defer func() { _ = token.Close() }()
	var need uint32
	_ = syscall.GetTokenInformation(token, syscall.TokenPrivileges, nil, 0, &need)
	buf := make([]byte, need+64)
	if err := syscall.GetTokenInformation(token, syscall.TokenPrivileges, &buf[0], uint32(len(buf)), &need); err != nil {
		t.Logf("  GetTokenInformation(TokenPrivileges) = %v", err)
		return
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	t.Logf("  token holds %d privileges", count)
	for i := range count {
		off := 4 + uintptr(i)*12
		low := *(*uint32)(unsafe.Pointer(&buf[off]))
		high := *(*int32)(unsafe.Pointer(&buf[off+4]))
		attrs := *(*uint32)(unsafe.Pointer(&buf[off+8]))
		luid := struct {
			Low  uint32
			High int32
		}{low, high}
		name := make([]uint16, 128)
		size := uint32(len(name))
		got, _, callErr := probeProcLookupPrivilegeName.Call(0, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&size)))
		rendered := "<lookup-failed:" + callErr.Error() + ">"
		if got != 0 {
			rendered = syscall.UTF16ToString(name[:size])
		}
		t.Logf("    %-40s attrs=0x%08x", rendered, attrs)
	}
}

func probeSaclAttempt(t *testing.T, dir string) {
	t.Helper()
	const saclSecurityInformation uintptr = 0x00000008
	const labelSecurityInformation uintptr = 0x00000010
	attempt := func(label string, info uintptr) {
		namep, _ := syscall.UTF16PtrFromString(dir)
		var acl *aclHeader
		var descriptor syscall.Handle
		status, _, _ := procGetNamedSecurityInfo.Call(
			uintptr(unsafe.Pointer(namep)), seFileObject, info,
			0, 0, 0, uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&descriptor)))
		if status != errorSuccess {
			t.Logf("  %s on %s: status=%d (%v)", label, dir, status, syscall.Errno(status))
			return
		}
		defer freeDescriptor(descriptor)
		if acl == nil {
			t.Logf("  %s on %s: SUCCESS, list is NULL (absent)", label, dir)
			return
		}
		t.Logf("  %s on %s: SUCCESS, rev=%d aceCount=%d", label, dir, acl.AclRevision, acl.AceCount)
		for index := range uint32(acl.AceCount) {
			ace, ok := aceAt(acl, index)
			if !ok {
				continue
			}
			t.Logf("    #%d type=0x%02x flags=0x%02x mask=0x%08x", index, ace.AceType, ace.AceFlags, ace.Mask)
		}
	}
	attempt("SACL_SECURITY_INFORMATION", saclSecurityInformation)
	attempt("LABEL_SECURITY_INFORMATION", labelSecurityInformation)

	// Try to enable SeSecurityPrivilege, then retry the SACL read.
	namep, _ := syscall.UTF16PtrFromString("SeSecurityPrivilege")
	var luid struct {
		Low  uint32
		High int32
	}
	got, _, callErr := probeProcLookupPrivilegeValue.Call(0, uintptr(unsafe.Pointer(namep)), uintptr(unsafe.Pointer(&luid)))
	if got == 0 {
		t.Logf("  LookupPrivilegeValueW(SeSecurityPrivilege) failed: %v", callErr)
		return
	}
	var token syscall.Token
	proc, _ := syscall.GetCurrentProcess()
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_ADJUST_PRIVILEGES|syscall.TOKEN_QUERY, &token); err != nil {
		t.Logf("  OpenProcessToken(ADJUST) = %v", err)
		return
	}
	defer func() { _ = token.Close() }()
	newState := struct {
		Count uint32
		Luid  struct{ Low, High uint32 }
		Attrs uint32
	}{Count: 1, Attrs: 0x00000002}
	newState.Luid.Low = luid.Low
	newState.Luid.High = uint32(luid.High)
	adjusted, _, adjErr := probeProcAdjustTokenPriv.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&newState)), 0, 0, 0)
	t.Logf("  AdjustTokenPrivileges(SeSecurityPrivilege ENABLE) ret=%d lastErr=%v", adjusted, adjErr)
	attempt("SACL after enable", saclSecurityInformation)
}

// probeSetAcl builds a DACL by hand and applies it, to find out whether an
// NTFS directory will STORE an object-type ACE.
func probeSetAcl(t *testing.T, dir string, revision byte, aceType byte) {
	t.Helper()
	sidp, sidErr := syscall.StringToSid(probeSidEveryone)
	if sidErr != nil {
		t.Logf("  StringToSid = %v", sidErr)
		return
	}
	sidLen := syscall.GetLengthSid(sidp)
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sidp)), sidLen)

	// ACCESS_ALLOWED_ACE : header(4) + mask(4) + sid
	// ACCESS_ALLOWED_OBJECT_ACE : header(4) + mask(4) + flags(4) + sid (no GUIDs, Flags=0)
	extra := 0
	if aceType == 0x05 || aceType == 0x06 {
		extra = 4
	}
	aceSize := 8 + extra + int(sidLen)
	aclSize := 8 + aceSize
	buf := make([]byte, aclSize)
	buf[0] = revision
	*(*uint16)(unsafe.Pointer(&buf[2])) = uint16(aclSize)
	*(*uint16)(unsafe.Pointer(&buf[4])) = 1
	buf[8] = aceType
	buf[9] = 0
	*(*uint16)(unsafe.Pointer(&buf[10])) = uint16(aceSize)
	*(*uint32)(unsafe.Pointer(&buf[12])) = fileAddFile | fileAddSubdirectory
	if extra == 4 {
		*(*uint32)(unsafe.Pointer(&buf[16])) = 0 // Flags: no GUID present
	}
	copy(buf[16+extra:], sidBytes)

	// Does our own walk see it in memory, before the kernel is involved?
	memWritable, memWhy := walkDacl((*aclHeader)(unsafe.Pointer(&buf[0])))
	t.Logf("  hand-built rev=%d aceType=0x%02x : walkDacl(in memory) = %v (%q)", revision, aceType, memWritable, memWhy)

	namep, _ := syscall.UTF16PtrFromString(dir)
	const protectedDacl uintptr = 0x80000000
	status, _, _ := probeProcSetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(namep)), seFileObject, daclSecurityInformation|protectedDacl,
		0, 0, uintptr(unsafe.Pointer(&buf[0])), 0)
	t.Logf("  SetNamedSecurityInfoW rev=%d aceType=0x%02x -> status=%d (%v)", revision, aceType, status, syscall.Errno(status))
	if status != errorSuccess {
		return
	}
	probeDumpDacl(t, dir)
}

func TestZZProbeWindowsAclFacts(t *testing.T) {
	t.Logf("=== 1. DACLs of directories Windows ships ===")
	candidates := []string{
		os.Getenv("ProgramData"),
		os.Getenv("SystemRoot") + `\Temp`,
		os.Getenv("SystemRoot"),
		os.Getenv("TEMP"),
		`C:\`,
	}
	base := t.TempDir()
	child := filepath.Join(base, "locks")
	if err := os.Mkdir(child, 0o700); err == nil {
		candidates = append(candidates, base, child)
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			t.Logf("  [%s] absent: %v", dir, err)
			continue
		}
		probeDumpDacl(t, dir)
	}

	t.Logf("=== 2. token privileges on this runner ===")
	probeTokenPrivileges(t)

	t.Logf("=== 3. SACL / label readability ===")
	probeSaclAttempt(t, base)

	t.Logf("=== 4. can an NTFS directory store an object-type ACE? ===")
	for _, c := range []struct {
		rev     byte
		aceType byte
	}{{2, 0x00}, {2, 0x05}, {4, 0x05}} {
		sub := filepath.Join(base, fmt.Sprintf("acl-r%d-t%d", c.rev, c.aceType))
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Logf("  mkdir %s = %v", sub, err)
			continue
		}
		probeSetAcl(t, sub, c.rev, c.aceType)
	}

	t.Fatalf("PROBE — deliberate failure so the measurements above are printed by a lane that runs without -v")
}
