// Package memlimit — resolving WHICH cgroup limit files govern this process:
// where each hierarchy is mounted, which cgroup the process belongs to, and the
// ancestors whose caps bound it just as effectively.
package memlimit

import (
	"path"
	"slices"
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
	v2Root, v1Root := resolveMountPoints(readFile)

	content, err := readFile(procSelfCgroup)
	//: No /proc means no cgroups: any non-Linux host. Fall back to the mount
	//: roots so a namespaced container still works if membership is
	//: unreadable but the filesystem is present.
	if err != nil {
		//: Deliver the resolved roots.
		return []string{
			path.Join(v2Root, cgroupV2LimitFile),
			path.Join(v1Root, cgroupV1LimitFile),
		}
	}

	v2Path, v1Path := parseCgroupMembership(string(content))

	candidates := make([]string, 0, defaultCandidateCapacity)
	candidates = append(candidates, ancestorLimitFiles(v2Root, v2Path, cgroupV2LimitFile)...)
	candidates = append(candidates, ancestorLimitFiles(v1Root, v1Path, cgroupV1LimitFile)...)

	//: Deliver the ordered candidates to the caller.
	return candidates
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

// ancestorLimitFiles returns the limit file for cgroupPath under root, then
// for each ancestor up to the root itself. Returns nil when the hierarchy is
// absent, so a v1-only or v2-only host contributes candidates from one side.
func ancestorLimitFiles(root, cgroupPath, limitFile string) []string {
	//: An empty path means this hierarchy was not listed for the process.
	if cgroupPath == "" {
		//: Contribute nothing for an absent hierarchy.
		return nil
	}

	cleaned := path.Clean(rootCgroupPath + cgroupPath)
	var files []string
	//: Walk outward until the root is consumed, so a restrictive ancestor
	//: is considered even when the process's own cgroup is unlimited.
	for {
		files = append(files, path.Join(root, cleaned, limitFile))
		//: path.Dir("/") is "/" — stop before looping forever.
		if cleaned == rootCgroupPath {
			break
		}
		cleaned = path.Dir(cleaned)
	}

	//: Deliver the ordered ancestor files to the caller.
	return files
}

// resolveMountPoints returns where the unified hierarchy and the v1 memory
// controller are attached, falling back to the conventional locations when
// /proc/self/mountinfo is unreadable or lists neither.
//
// Limits are read relative to the mount point, so assuming /sys/fs/cgroup
// silently reads nothing on a host that mounts the hierarchy elsewhere.
func resolveMountPoints(readFile func(string) ([]byte, error)) (v2Root, v1Root string) {
	v2Root, v1Root = cgroupMountRoot, cgroupV1MemoryRoot

	content, err := readFile(procSelfMountinfo)
	//: Unreadable mountinfo leaves the conventional locations in place.
	if err != nil {
		//: Deliver the fallbacks to the caller.
		return v2Root, v1Root
	}

	//: Scan every mount: a hybrid host attaches both hierarchies.
	for line := range strings.SplitSeq(string(content), "\n") {
		mountPoint, fstype, superOptions, ok := parseMountinfoLine(line)
		//: Malformed or irrelevant lines carry no mount to record.
		if !ok {
			continue
		}
		//: The unified hierarchy has its own filesystem type.
		if fstype == fstypeCgroupV2 {
			v2Root = mountPoint
			continue
		}
		//: A v1 mount is relevant only when it carries the memory controller.
		if fstype == fstypeCgroupV1 && slices.Contains(strings.Split(superOptions, ","), v1MemoryController) {
			v1Root = mountPoint
		}
	}

	//: Deliver the resolved mount points to the caller.
	return v2Root, v1Root
}

// parseMountinfoLine extracts the mount point, filesystem type and super-block
// options from one /proc/self/mountinfo line.
//
// The prefix carries a variable number of optional fields, so the " - "
// separator is the only reliable way to find where the suffix begins.
func parseMountinfoLine(line string) (mountPoint, fstype, superOptions string, ok bool) {
	prefix, suffix, found := strings.Cut(line, mountinfoSeparator)
	//: A line without the separator is not a mount entry.
	if !found {
		//: Signal absence to the caller.
		return "", "", "", false
	}

	prefixFields := strings.Fields(prefix)
	suffixFields := strings.Fields(suffix)
	//: Both halves must be long enough to carry the fields we read.
	if len(prefixFields) <= mountpointField || len(suffixFields) < suffixFieldCount {
		//: Signal absence to the caller.
		return "", "", "", false
	}

	//: Deliver the three fields the caller needs.
	return prefixFields[mountpointField], suffixFields[fstypeField], suffixFields[superOptionsField], true
}
