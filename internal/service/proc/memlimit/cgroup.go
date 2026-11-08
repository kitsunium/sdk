package memlimit

import (
	"path"
	"slices"

	"github.com/kitsunium/sdk/internal/service/proc/internal/cgroupfs"
)

// procSelfCgroup names the per-process cgroup membership file (cgroupfs's).
const procSelfCgroup string = cgroupfs.SelfCgroupFile

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
// Reading only the hierarchy's ROOT CGROUP was wrong — a different sense of
// "root" from cgroupMount.root, which is what a mount exposes. Limits belong to
// cgroup directories, not to the mount. A process in a child cgroup — the normal
// shape under systemd, and under any runtime that does not use cgroup namespaces
// — would see the root's unlimited value and conclude there was no cap, silently
// disabling the feature exactly where it is needed.
//
// Ancestors are included because a restrictive parent bounds the process just
// as effectively as its own cgroup; the caller takes the minimum.
func resolveCgroupPaths(readFile func(string) ([]byte, error)) []string {
	//: Where each hierarchy is attached is discoverable; hard-coding
	//: /sys/fs/cgroup silently misses any host that mounts it elsewhere.
	v2Mounts, v1Mounts := cgroupfs.Mounts(readFile)

	content, err := readFile(procSelfCgroup)
	//: No /proc means no cgroups: any non-Linux host. Fall back to the mount
	//: points so a namespaced container still works if membership is
	//: unreadable but the filesystem is present.
	if err != nil {
		//: Deliver the resolved mount points.
		return mountPointFiles(v2Mounts, v1Mounts)
	}

	v2Path, v1Path := cgroupfs.Membership(string(content))

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
func mountPointFiles(v2Mounts, v1Mounts []cgroupfs.MountValue) []string {
	files := make([]string, 0, len(v2Mounts)+len(v1Mounts))
	//: The unified hierarchy first, matching the ordinary candidate order.
	for _, mount := range v2Mounts {
		files = append(files, path.Join(mount.Point, cgroupV2LimitFile))
	}
	//: Then the legacy one.
	for _, mount := range v1Mounts {
		files = append(files, path.Join(mount.Point, cgroupV1LimitFile))
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

// ancestorLimitFiles returns the limit file for cgroupPath under mount, then
// for each ancestor up to the exposed root itself. Returns nil when the
// hierarchy is absent, so a v1-only or v2-only host contributes candidates from
// one side, and nil when the mount exposes a subtree this process is not in.
func ancestorLimitFiles(mount cgroupfs.MountValue, cgroupPath, limitFile string) []string {
	//: An empty path means this hierarchy was not listed for the process.
	if cgroupPath == "" {
		//: Contribute nothing for an absent hierarchy.
		return nil
	}

	cleaned, reachable := cgroupfs.UnderMountRoot(mount.Root, path.Clean(rootCgroupPath+cgroupPath))
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
		files = append(files, path.Join(mount.Point, cleaned, limitFile))
		//: path.Dir("/") is "/" — stop before looping forever.
		if cleaned == rootCgroupPath {
			break
		}
		cleaned = path.Dir(cleaned)
	}

	//: Deliver the ordered ancestor files to the caller.
	return files
}
