package self

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/internal/cgroupfs"
)

const (
	// StartSourceInit is the package's initialisation: within it, not the kernel's
	// word.
	StartSourceInit StartSource = iota + 1
	// StartSourceKernel is the kernel's record of when it started the process.
	StartSourceKernel
)

// The marks of a Go build id where the linker writes it raw, at the start of
// the text of every binary format (cmd/internal/buildid): the ELF note holds
// the same id.
const (
	buildIDPrefix    string = "\xff Go build ID: \""
	buildIDSuffix    string = "\"\n \xff"
	buildIDScanBytes int64  = 32 << 10
	goBuildIDNote    string = ".note.go.buildid"
	noteHeaderBytes  int    = 16
	bootIDFile       string = "/proc/sys/kernel/random/boot_id"
)

// containerPrefixes are the scope prefixes the container runtimes give a
// container's cgroup: docker, containerd's CRI plugin, CRI-O and podman.
var containerPrefixes = [...]string{"docker-", "cri-containerd-", "crio-", "libpod-"}

// StartSource names where IdentityValue.StartedAt came from. The zero is
// unminted.
type StartSource uint8

// String renders the source for a log line, "unknown" for a value never minted.
func (s StartSource) String() string {
	switch s {
	//: the package's own clock.
	case StartSourceInit:
		return "init"
	//: the kernel's.
	case StartSourceKernel:
		return "kernel"
	//: never minted.
	default:
		return "unknown"
	}
}

// IdentityValue is what the kernel says the running process is. Every field is
// a zero where the platform does not give it, never an error (ADR 0100).
type IdentityValue struct {
	// StartedAt is when the process started: the kernel's record on Linux
	// (/proc/self/stat's start tick after /proc/stat's boot time) and Windows
	// (GetProcessTimes), the package's initialisation elsewhere —
	// StartedAtSource says which.
	StartedAt time.Time
	// Executable is the path of the running binary, as os.Executable reads it.
	Executable string
	// BuildID is the Go build id of the running binary — its ELF
	// .note.go.buildid, or the raw mark the linker writes at the start of the
	// text of every format — what `go tool buildid` prints; empty when absent.
	BuildID string
	// ContainerID is the 64-hex id of the container the process runs in, read
	// from its cgroup path in the docker, cri-containerd, crio, libpod and
	// kubepods shapes; empty outside one.
	ContainerID string
	// BootID is the kernel's id of the current boot (Linux); empty elsewhere.
	BootID string
	// PIDNamespace is the inode of /proc/self/ns/pid, the pid namespace's id
	// (Linux); zero elsewhere.
	PIDNamespace uint64
	// Digest is the SHA-256 of the running binary's file, valid when DigestOK.
	Digest [32]byte
	// StartedAtSource says where StartedAt came from.
	StartedAtSource StartSource
	// DigestOK reports that the binary could be read whole and Digest holds its hash.
	DigestOK bool
	// NSPid1 reports that the process is pid 1 of its pid namespace (the last
	// NSpid entry of /proc/self/status) — the init of a container.
	NSPid1 bool
}

// ReadIdentity reads what the kernel says the running process is — facts the
// runtime cannot give: when the kernel started it (restart detection, a
// pid-reuse-safe identity), the SHA-256 and Go build id of the binary really
// running (self-verification before a signed self-update replaces it), the
// container and pid namespace it runs in, and the boot. Like ReadStats it
// never fails: a fact the platform does not give is zero, and the flags say
// which. Hashing the binary reads it whole: call it once, not per request.
func ReadIdentity() IdentityValue {
	identity := IdentityValue{StartedAt: started, StartedAtSource: StartSourceInit}
	//: the binary, its digest and its build id.
	if exe, err := os.Executable(); err == nil {
		identity.Executable = exe
		identity.Digest, identity.DigestOK = digestOf(exe)
		identity.BuildID = buildIDOf(exe)
	}
	//: the kernel's start time, where the platform gives it.
	if at, ok := kernelStart(); ok {
		identity.StartedAt, identity.StartedAtSource = at, StartSourceKernel
	}
	//: the container, from the cgroup membership.
	if membership, err := os.ReadFile(cgroupfs.SelfCgroupFile); err == nil {
		v2Path, v1Path := cgroupfs.Membership(string(membership))
		identity.ContainerID = containerOf(v2Path)
		//: a v1-only host names it on the memory controller's line.
		if identity.ContainerID == "" {
			identity.ContainerID = containerOf(v1Path)
		}
	}
	identity.PIDNamespace, identity.NSPid1 = pidNamespace()
	//: the boot, where the kernel names it.
	if boot, err := os.ReadFile(bootIDFile); err == nil {
		identity.BootID = strings.TrimSpace(string(boot))
	}
	//: every field read, the rest zero.
	return identity
}

// digestOf hashes the file at name.
func digestOf(name string) (sum [sha256.Size]byte, ok bool) {
	f, err := os.Open(name)
	//: unreadable: no digest.
	if err != nil {
		return sum, false
	}
	defer func() {
		//: a close failure on a read-only file changes nothing the digest says.
		if cerr := f.Close(); cerr != nil {
			return
		}
	}()
	hash := sha256.New()
	//: a read error mid-file is no digest either.
	if _, err := io.Copy(hash, f); err != nil {
		return sum, false
	}
	copy(sum[:], hash.Sum(nil))
	//: the digest.
	return sum, true
}

// buildIDOf reads the Go build id: the ELF note where there is one, the raw
// mark at the start of the text otherwise, and "" when neither is there.
func buildIDOf(name string) string {
	//: an ELF binary's note.
	if id, ok := elfNoteBuildID(name); ok {
		return id
	}
	//: the raw mark, every format.
	if id, ok := rawBuildID(name); ok {
		return id
	}
	//: a binary without a Go build id.
	return ""
}

// elfNoteBuildID reads the .note.go.buildid note of an ELF binary.
func elfNoteBuildID(name string) (id string, ok bool) {
	f, err := elf.Open(name)
	//: not ELF.
	if err != nil {
		return "", false
	}
	defer closeQuietly(f)
	section := f.Section(goBuildIDNote)
	//: a binary without the note.
	if section == nil {
		return "", false
	}
	note, err := section.Data()
	//: namesz, descsz, type, the name "Go\x00\x00", then the id.
	if err != nil || len(note) < noteHeaderBytes {
		return "", false
	}
	descSize := int(f.ByteOrder.Uint32(note[4:8]))
	//: a note shorter than it says.
	if len(note) < noteHeaderBytes+descSize {
		return "", false
	}
	//: the id.
	return string(note[noteHeaderBytes : noteHeaderBytes+descSize]), true
}

// rawBuildID scans the start of the file for the linker's raw mark.
func rawBuildID(name string) (id string, ok bool) {
	f, err := os.Open(name)
	//: unreadable.
	if err != nil {
		return "", false
	}
	defer func() {
		//: a close failure on a read-only file changes nothing the id says.
		if cerr := f.Close(); cerr != nil {
			return
		}
	}()
	head, err := io.ReadAll(io.LimitReader(f, buildIDScanBytes))
	//: a read error is no id.
	if err != nil {
		return "", false
	}
	_, after, found := bytes.Cut(head, []byte(buildIDPrefix))
	//: no mark.
	if !found {
		return "", false
	}
	raw, _, closed := bytes.Cut(after, []byte(buildIDSuffix))
	//: a mark cut by the scan's end.
	if !closed {
		return "", false
	}
	//: the id.
	return string(raw), true
}

// closeQuietly closes a file read to the end: a close failure on a read-only
// file changes nothing the reading can say.
func closeQuietly(f io.Closer) {
	//: nothing is held past this point either way.
	if err := f.Close(); err != nil {
		return
	}
}

// containerOf finds a container id in a cgroup path: the deepest element that
// is 64 hex digits once a runtime's scope prefix and ".scope" are dropped.
func containerOf(cgroupPath string) string {
	//: from the deepest element up.
	for element := path.Base(cgroupPath); element != "/" && element != "." && element != ""; {
		candidate := strings.TrimSuffix(element, ".scope")
		//: a runtime's prefix.
		for _, prefix := range containerPrefixes {
			candidate = strings.TrimPrefix(candidate, prefix)
		}
		//: a full container id.
		if isContainerID(candidate) {
			return candidate
		}
		cgroupPath = path.Dir(cgroupPath)
		element = path.Base(cgroupPath)
	}
	//: not a container's cgroup.
	return ""
}

// isContainerID reports a 64-digit lower-case hex string.
func isContainerID(s string) bool {
	//: the length of a sha256 in hex.
	if len(s) != 2*sha256.Size {
		return false
	}
	//: hex digits only.
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
