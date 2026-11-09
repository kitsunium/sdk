package cgroupfs

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// SelfCgroupFile names the per-process cgroup membership file. Each line maps
// a hierarchy to the path this process belongs to, relative to that
// hierarchy's mount root.
const SelfCgroupFile string = "/proc/self/cgroup"

// rootCgroupPath is the path a process reports when it sits at the hierarchy
// root — the usual case inside a cgroup namespace.
const rootCgroupPath string = "/"

// SelfMountinfoFile lists this process's mounts, including where each cgroup
// hierarchy is actually attached.
const SelfMountinfoFile string = "/proc/self/mountinfo"

// mountinfoSeparator delimits the variable-length prefix from the fstype and
// source fields. The prefix has an optional-fields section of unbounded
// length, so the separator is the only reliable anchor.
const mountinfoSeparator string = " - "

// mountRootField is the index, in the prefix fields, of the mount's root
// WITHIN its filesystem. A runtime that bind-mounts a container's own cgroup
// subtree reports that subtree here, and a membership path read from
// /proc/self/cgroup names a real file only once it is expressed relative to it.
const mountRootField int = 3

// mountpointField is the index of the mount point in the prefix fields.
const mountpointField int = 4

// fstypeField is the index of the filesystem type after the separator.
const fstypeField int = 0

// superOptionsField is the index of the super-block options after the
// separator, where a v1 mount lists the controllers it carries.
const superOptionsField int = 2

// fstypeCgroupV2 is the filesystem type of the unified hierarchy.
const fstypeCgroupV2 string = "cgroup2"

// fstypeCgroupV1 is the filesystem type of a legacy controller mount.
const fstypeCgroupV1 string = "cgroup"

// suffixFieldCount is the minimum number of fields expected after the
// separator: fstype, source, and super options.
const suffixFieldCount int = 3

// mangleEscape is the byte the kernel's mangle_path writes before an octal
// triple. Space, tab, newline and backslash cannot appear literally in a
// whitespace-delimited field, so the kernel encodes them.
const mangleEscape byte = '\\'

// mangleDigits is how many octal digits follow mangleEscape. The kernel always
// writes three, zero-padded.
const mangleDigits int = 3

// octalBase is the base mangle_path encodes an escaped byte in.
const octalBase int = 8

// escapedByteBits is the width one decoded escape parses into: a single byte.
const escapedByteBits int = 8

// v2LinePrefix marks the unified-hierarchy line in /proc/self/cgroup.
const v2LinePrefix string = "0::"

// v1MemoryController is the controller name to match on a v1 line.
const v1MemoryController string = "memory"

// cgroupLineFields is the number of colon-separated fields per line:
// hierarchy ID, comma-separated controllers, and the path.
const cgroupLineFields int = 3

// controllersField is the index of the comma-separated controller list.
const controllersField int = 1

// cgroupPathField is the index of the membership path.
const cgroupPathField int = 2

// cgroupMountRoot is where the unified hierarchy is conventionally mounted.
// Used as a fallback when /proc/self/mountinfo cannot be read.
const cgroupMountRoot string = "/sys/fs/cgroup"

// cgroupV1MemoryRoot is the v1 memory controller's conventional mount point,
// used on the same fallback path.
const cgroupV1MemoryRoot string = "/sys/fs/cgroup/memory"

// MountValue is one attached cgroup hierarchy: where it is mounted, and which
// part of the cgroup filesystem that mount exposes. A whole-hierarchy mount
// exposes "/"; a bind mount of a subtree exposes that subtree, and a
// membership path has to be re-expressed relative to it (UnderMountRoot).
type MountValue struct {
	// Point is the directory the hierarchy is attached to.
	Point string
	// Root is the path WITHIN the cgroup filesystem this mount exposes.
	Root string
}

// Membership extracts this process's path in the unified hierarchy and in the
// v1 memory controller from /proc/self/cgroup's content. Either is empty when
// the hierarchy is not listed.
func Membership(content string) (v2Path, v1Path string) {
	//: Scan every line: a host can mount both hierarchies at once.
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		//: Blank trailing lines carry no membership.
		if trimmed == "" {
			continue
		}
		//: The unified hierarchy is the only one with an empty controller
		//: field, which is what the "0::" prefix encodes.
		if after, found := strings.CutPrefix(trimmed, v2LinePrefix); found {
			v2Path = after
			continue
		}
		//: Any remaining line may carry the v1 memory controller.
		if memPath, found := v1MemoryPath(trimmed); found {
			v1Path = memPath
		}
	}

	//: Return the computed result to the caller.
	return v2Path, v1Path
}

// v1MemoryPath returns the path from a v1 line whose controller list includes
// "memory". Controllers are comma-separated because a hierarchy can carry
// several co-mounted controllers at once.
func v1MemoryPath(line string) (cgroupPath string, ok bool) {
	parts := strings.SplitN(line, ":", cgroupLineFields)
	//: A malformed line cannot be interpreted.
	if len(parts) != cgroupLineFields {
		//: Signal absence to the caller.
		return "", false
	}

	//: Match the controller exactly — "memory" must not match "memory+swap"
	//: or any other name that merely contains it.
	if slices.Contains(strings.Split(parts[controllersField], ","), v1MemoryController) {
		//: Deliver the membership path.
		return parts[cgroupPathField], true
	}

	//: This line governs some other controller.
	return "", false
}

// UnderMountRoot re-expresses a /proc/self/cgroup membership path relative to
// the part of the cgroup filesystem a mount exposes (mountinfo field 4),
// reporting false when the mount does not expose this process's cgroup.
func UnderMountRoot(mountRoot, cgroupPath string) (relative string, ok bool) {
	exposed := path.Clean(rootCgroupPath + mountRoot)
	//: A whole-hierarchy mount exposes every cgroup path unchanged.
	if exposed == rootCgroupPath {
		//: Deliver the membership path untouched.
		return cgroupPath, true
	}
	//: The process sits exactly at the exposed subtree: it IS the mount point.
	if cgroupPath == exposed {
		//: Deliver the mount point itself.
		return rootCgroupPath, true
	}
	//: Below the exposed subtree: drop the prefix and keep the separator, so
	//: path.Join under the mount point lands on the right directory.
	if below, found := strings.CutPrefix(cgroupPath, exposed+rootCgroupPath); found {
		//: Deliver the path relative to what the mount exposes.
		return rootCgroupPath + below, true
	}

	//: Outside the exposed subtree entirely: this mount reaches nothing of ours.
	return "", false
}

// Mounts returns every attachment of the unified hierarchy and of the v1
// memory controller, with what each exposes, falling back to the
// conventional /sys/fs/cgroup and /sys/fs/cgroup/memory when mountinfo is
// unreadable or lists neither.
func Mounts(readFile func(string) ([]byte, error)) (v2, v1 []MountValue) {
	content, err := readFile(SelfMountinfoFile)
	//: Unreadable mountinfo leaves only the conventional locations below.
	if err == nil {
		v2, v1 = scanCgroupMounts(string(content))
	}

	//: A host that lists no unified mount may still have one where convention
	//: puts it, which is what a namespaced container relies on.
	if len(v2) == 0 {
		v2 = []MountValue{{Point: cgroupMountRoot, Root: rootCgroupPath}}
	}
	//: The legacy hierarchy gets the same treatment.
	if len(v1) == 0 {
		v1 = []MountValue{{Point: cgroupV1MemoryRoot, Root: rootCgroupPath}}
	}

	//: Deliver the resolved mounts to the caller.
	return v2, v1
}

// scanCgroupMounts extracts every cgroup mount from /proc/self/mountinfo
// content, split by hierarchy.
func scanCgroupMounts(content string) (v2, v1 []MountValue) {
	//: Scan every mount: a hybrid host attaches both hierarchies.
	for line := range strings.SplitSeq(content, "\n") {
		mount, fstype, superOptions, ok := parseMountinfoLine(line)
		//: Malformed or irrelevant lines carry no mount to record.
		if !ok {
			continue
		}
		//: The unified hierarchy has its own filesystem type.
		if fstype == fstypeCgroupV2 {
			v2 = append(v2, mount)
			continue
		}
		//: A v1 mount is relevant only when it carries the memory controller.
		if fstype == fstypeCgroupV1 && slices.Contains(strings.Split(superOptions, ","), v1MemoryController) {
			v1 = append(v1, mount)
		}
	}

	//: Deliver both hierarchies' mounts to the caller.
	return v2, v1
}

// parseMountinfoLine extracts the mount, its filesystem type and its
// super-block options from one /proc/self/mountinfo line.
//
// The prefix carries a variable number of optional fields, so the " - "
// separator is the only reliable way to find where the suffix begins.
func parseMountinfoLine(line string) (mount MountValue, fstype, superOptions string, ok bool) {
	prefix, suffix, found := strings.Cut(line, mountinfoSeparator)
	//: A line without the separator is not a mount entry.
	if !found {
		//: Signal absence to the caller.
		return MountValue{}, "", "", false
	}

	prefixFields := strings.Fields(prefix)
	suffixFields := strings.Fields(suffix)
	//: Both halves must be long enough to carry the fields we read.
	if len(prefixFields) <= mountpointField || len(suffixFields) < suffixFieldCount {
		//: Signal absence to the caller.
		return MountValue{}, "", "", false
	}

	//: Both path fields pass through mangle_path on the way out of the kernel;
	//: the membership path in /proc/self/cgroup does NOT, so the decode belongs
	//: here and nowhere else.
	mount = MountValue{
		Point: unmangleMountinfoPath(prefixFields[mountpointField]),
		Root:  unmangleMountinfoPath(prefixFields[mountRootField]),
	}

	//: Deliver the fields the caller needs.
	return mount, suffixFields[fstypeField], suffixFields[superOptionsField], true
}

// unmangleMountinfoPath decodes the octal escapes the kernel's mangle_path
// writes into a mountinfo path field.
//
// The fields are whitespace-delimited, so space, tab, newline and backslash are
// emitted as \040, \011, \012 and \134. Kept verbatim, a cgroup filesystem
// mounted at a path containing one of them yields a candidate no file answers
// to. Any three-digit octal triple is decoded rather than only those four: the
// encoder escapes its own backslash, so a literal "\040" cannot reach this
// function undecoded.
func unmangleMountinfoPath(field string) string {
	//: No escape marker means nothing to decode — every ordinary path.
	if !strings.ContainsRune(field, rune(mangleEscape)) {
		//: Deliver the field untouched, without allocating.
		return field
	}

	var decoded strings.Builder
	decoded.Grow(len(field))
	//: Walk bytes rather than runes: an escape is three ASCII digits and a
	//: decoded byte may be a UTF-8 continuation that is not a rune on its own.
	for index := 0; index < len(field); {
		escape, width := decodeMangledByte(field, index)
		decoded.WriteByte(escape)
		index += width
	}

	//: Deliver the decoded path.
	return decoded.String()
}

// decodeMangledByte returns the byte at index and how many input bytes it
// consumed: four for a well-formed octal escape, one for anything else.
func decodeMangledByte(field string, index int) (value byte, width int) {
	//: An escape needs the marker plus three digits still ahead of it.
	if field[index] != mangleEscape || index+mangleDigits >= len(field) {
		//: Not an escape: carry the byte through.
		return field[index], 1
	}

	parsed, err := strconv.ParseUint(field[index+1:index+1+mangleDigits], octalBase, escapedByteBits)
	//: A backslash the kernel did not write as an escape stays verbatim rather
	//: than swallowing the three bytes behind it.
	if err != nil {
		//: Carry the marker through as an ordinary byte.
		return field[index], 1
	}

	//: Deliver the decoded byte and the whole escape's width.
	return byte(parsed), 1 + mangleDigits
}

// OwnGroup returns the directory of the unified-hierarchy cgroup this process
// belongs to: the "0::" path of /proc/self/cgroup, re-expressed under the
// first cgroup2 mount that exposes it. It reports false on a host with no
// unified membership — not Linux, a v1-only host — or with no mount reaching
// it.
func OwnGroup(readFile func(string) ([]byte, error)) (dir string, ok bool) {
	content, err := readFile(SelfCgroupFile)
	//: no membership file: not Linux.
	if err != nil {
		//: nothing to resolve.
		return "", false
	}
	v2Path, _ := Membership(string(content))
	//: no unified line: a v1-only host.
	if v2Path == "" {
		//: nothing to resolve.
		return "", false
	}
	v2Mounts, _ := Mounts(readFile)
	cleaned := path.Clean(rootCgroupPath + v2Path)
	//: the first mount that exposes this process's cgroup names its directory.
	for _, mount := range v2Mounts {
		//: a mount exposing a subtree this process is not in names nothing of ours.
		if relative, reachable := UnderMountRoot(mount.Root, cleaned); reachable {
			//: the directory under that mount point.
			return path.Join(mount.Point, relative), true
		}
	}
	//: no mount reaches it.
	return "", false
}
