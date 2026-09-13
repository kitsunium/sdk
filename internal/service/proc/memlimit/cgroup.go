// Package memlimit — resolving WHICH cgroup limit files govern this process:
// where each hierarchy is mounted, which part of the filesystem that mount
// exposes, which cgroup the process belongs to, and the ancestors whose caps
// bound it just as effectively.
package memlimit

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// procSelfCgroup names the per-process cgroup membership file. Each line maps
// a hierarchy to the path this process belongs to, relative to that
// hierarchy's mount root.
const procSelfCgroup string = "/proc/self/cgroup"

// cgroupMountRoot is where the unified hierarchy is conventionally mounted.
// Used as a fallback when /proc/self/mountinfo cannot be read.
const cgroupMountRoot string = "/sys/fs/cgroup"

// cgroupV1MemoryRoot is the v1 memory controller's conventional mount point,
// used on the same fallback path.
const cgroupV1MemoryRoot string = "/sys/fs/cgroup/memory"

// procSelfMountinfo lists this process's mounts, including where each cgroup
// hierarchy is actually attached.
const procSelfMountinfo string = "/proc/self/mountinfo"

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

// rootCgroupPath is the path a process reports when it sits at the hierarchy
// root — the usual case inside a cgroup namespace, where the container's own
// cgroup is presented as "/".
const rootCgroupPath string = "/"

// defaultCandidateCapacity pre-sizes the candidate slice for a typical nesting
// depth across both hierarchies.
const defaultCandidateCapacity int = 8

// resolveCgroupPaths returns the memory-limit files to consult, ordered from
// the process's own cgroup outward to the hierarchy root.
//
// Reading only the mount root was wrong: limits belong to cgroup directories,
// not to the mount. A process in a child cgroup — the normal shape under
// systemd, and under any runtime that does not use cgroup namespaces — would
// see the root's unlimited value and conclude there was no cap, silently
// disabling the feature exactly where it is needed.
//
// Ancestors are included because a restrictive parent bounds the process just
// as effectively as its own cgroup; the caller takes the minimum.
func resolveCgroupPaths(readFile func(string) ([]byte, error)) []string {
	//: Where each hierarchy is attached is discoverable; hard-coding
	//: /sys/fs/cgroup silently misses any host that mounts it elsewhere.
	v2Mounts, v1Mounts := resolveMounts(readFile)

	content, err := readFile(procSelfCgroup)
	//: No /proc means no cgroups: any non-Linux host. Fall back to the mount
	//: points so a namespaced container still works if membership is
	//: unreadable but the filesystem is present.
	if err != nil {
		//: Deliver the resolved mount points.
		return mountPointFiles(v2Mounts, v1Mounts)
	}

	v2Path, v1Path := parseCgroupMembership(string(content))

	candidates := make([]string, 0, defaultCandidateCapacity)
	//: Every mount of a hierarchy is consulted, not just the last one: two
	//: mounts at DIFFERENT points do not shadow each other, and one exposing a
	//: subtree cannot name the ancestors the other still can.
	for _, mount := range v2Mounts {
		candidates = append(candidates, ancestorLimitFiles(mount, v2Path, cgroupV2LimitFile)...)
	}
	//: The legacy hierarchy answers the same way.
	for _, mount := range v1Mounts {
		candidates = append(candidates, ancestorLimitFiles(mount, v1Path, cgroupV1LimitFile)...)
	}

	//: Deliver the ordered candidates to the caller.
	return dedupe(candidates)
}

// mountPointFiles returns the limit file directly under each mount point, which
// is the best guess available when the membership file cannot be read.
func mountPointFiles(v2Mounts, v1Mounts []cgroupMount) []string {
	files := make([]string, 0, len(v2Mounts)+len(v1Mounts))
	//: The unified hierarchy first, matching the ordinary candidate order.
	for _, mount := range v2Mounts {
		files = append(files, path.Join(mount.point, cgroupV2LimitFile))
	}
	//: Then the legacy one.
	for _, mount := range v1Mounts {
		files = append(files, path.Join(mount.point, cgroupV1LimitFile))
	}

	//: Deliver the mount-point files to the caller.
	return dedupe(files)
}

// dedupe returns paths with repeats removed, preserving first-seen order.
//
// Two mounts of one hierarchy can name the same file — a bind of a directory
// onto itself is the simplest way — and every repeat costs a redundant open for
// a value the caller would minimise against itself. The lists are a handful of
// entries deep, so a linear scan beats a map.
func dedupe(paths []string) []string {
	unique := make([]string, 0, len(paths))
	//: Keep the first occurrence and drop the rest.
	for _, candidate := range paths {
		//: A path already queued reads the same file twice.
		if slices.Contains(unique, candidate) {
			continue
		}
		unique = append(unique, candidate)
	}

	//: Deliver the deduplicated list.
	return unique
}

// cgroupMount is one attached cgroup hierarchy: where it is mounted, and which
// part of the cgroup filesystem that mount exposes.
//
// The two are not the same question. A whole-hierarchy mount exposes "/" and a
// membership path from /proc/self/cgroup names a file directly under the mount
// point. A bind mount of a subtree exposes that subtree, and the same membership
// path has to be re-expressed relative to it first.
type cgroupMount struct {
	// point is the directory the hierarchy is attached to.
	point string
	// root is the path WITHIN the cgroup filesystem this mount exposes.
	root string
}

// parseCgroupMembership extracts this process's path in the unified hierarchy
// and in the v1 memory controller. Either may be empty when the hierarchy is
// not mounted; both default to the root when present but unparseable.
func parseCgroupMembership(content string) (v2Path, v1Path string) {
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

// ancestorLimitFiles returns the limit file for cgroupPath under mount, then
// for each ancestor up to the exposed root itself. Returns nil when the
// hierarchy is absent, so a v1-only or v2-only host contributes candidates from
// one side, and nil when the mount exposes a subtree this process is not in.
func ancestorLimitFiles(mount cgroupMount, cgroupPath, limitFile string) []string {
	//: An empty path means this hierarchy was not listed for the process.
	if cgroupPath == "" {
		//: Contribute nothing for an absent hierarchy.
		return nil
	}

	cleaned, reachable := underMountRoot(mount.root, path.Clean(rootCgroupPath+cgroupPath))
	//: A mount exposing a subtree we are not in names no file of ours. Joining
	//: anyway would build a path that is either absent or ANOTHER cgroup's cap,
	//: and the caller takes a minimum, so a stranger's cap would win.
	if !reachable {
		//: Contribute nothing for a hierarchy this mount cannot reach.
		return nil
	}

	var files []string
	//: Walk outward until the root is consumed, so a restrictive ancestor
	//: is considered even when the process's own cgroup is unlimited.
	for {
		files = append(files, path.Join(mount.point, cleaned, limitFile))
		//: path.Dir("/") is "/" — stop before looping forever.
		if cleaned == rootCgroupPath {
			break
		}
		cleaned = path.Dir(cleaned)
	}

	//: Deliver the ordered ancestor files to the caller.
	return files
}

// underMountRoot re-expresses a /proc/self/cgroup membership path relative to
// the part of the cgroup filesystem a mount exposes, reporting false when the
// mount does not expose this process's cgroup at all.
//
// The two files disagree on purpose. /proc/self/cgroup names the cgroup
// relative to the reader's cgroup NAMESPACE; mountinfo field 3 names the mount's
// root relative to the same namespace. When a runtime bind-mounts a container's
// own subtree at /sys/fs/cgroup without a cgroup namespace — Docker's
// --cgroupns=host — the two are "/docker/abc" and "/docker/abc", and joining
// them verbatim repeats the subtree and names a file no cgroup answers to.
//
// A mount whose root sits ABOVE the namespace root is rendered by the kernel
// with ".." components ("/../../.." on a 6.12 kernel); path.Clean folds those
// back to "/", which is the identity translation and exactly what shipped
// before.
func underMountRoot(mountRoot, cgroupPath string) (relative string, ok bool) {
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

// resolveMounts returns EVERY attachment of the unified hierarchy and of the v1
// memory controller, and what each exposes, falling back to the conventional
// locations when /proc/self/mountinfo is unreadable or lists neither.
//
// Limits are read relative to the mount point, so assuming /sys/fs/cgroup
// silently reads nothing on a host that mounts the hierarchy elsewhere.
//
// Every attachment is kept rather than the last one, because two mounts of one
// hierarchy at DIFFERENT points do not shadow each other: a bind exposing only
// this process's own subtree cannot name the ancestors a whole-hierarchy mount
// still can, and discarding the latter would hide a restrictive parent. A mount
// genuinely stacked on another needs no rule — the covered mount's paths simply
// stop resolving, so its candidates read as absent.
func resolveMounts(readFile func(string) ([]byte, error)) (v2, v1 []cgroupMount) {
	content, err := readFile(procSelfMountinfo)
	//: Unreadable mountinfo leaves only the conventional locations below.
	if err == nil {
		v2, v1 = scanCgroupMounts(string(content))
	}

	//: A host that lists no unified mount may still have one where convention
	//: puts it, which is what a namespaced container relies on.
	if len(v2) == 0 {
		v2 = []cgroupMount{{point: cgroupMountRoot, root: rootCgroupPath}}
	}
	//: The legacy hierarchy gets the same treatment.
	if len(v1) == 0 {
		v1 = []cgroupMount{{point: cgroupV1MemoryRoot, root: rootCgroupPath}}
	}

	//: Deliver the resolved mounts to the caller.
	return v2, v1
}

// scanCgroupMounts extracts every cgroup mount from /proc/self/mountinfo
// content, split by hierarchy.
func scanCgroupMounts(content string) (v2, v1 []cgroupMount) {
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
func parseMountinfoLine(line string) (mount cgroupMount, fstype, superOptions string, ok bool) {
	prefix, suffix, found := strings.Cut(line, mountinfoSeparator)
	//: A line without the separator is not a mount entry.
	if !found {
		//: Signal absence to the caller.
		return cgroupMount{}, "", "", false
	}

	prefixFields := strings.Fields(prefix)
	suffixFields := strings.Fields(suffix)
	//: Both halves must be long enough to carry the fields we read.
	if len(prefixFields) <= mountpointField || len(suffixFields) < suffixFieldCount {
		//: Signal absence to the caller.
		return cgroupMount{}, "", "", false
	}

	//: Both path fields pass through mangle_path on the way out of the kernel;
	//: the membership path in /proc/self/cgroup does NOT, so the decode belongs
	//: here and nowhere else.
	mount = cgroupMount{
		point: unmangleMountinfoPath(prefixFields[mountpointField]),
		root:  unmangleMountinfoPath(prefixFields[mountRootField]),
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
