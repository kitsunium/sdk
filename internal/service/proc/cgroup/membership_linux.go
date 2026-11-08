package cgroup

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/internal/cgroupfs"
)

// membership reads this process's mounts and re-expresses dir under the one
// that holds it.
func membership(dir string) (string, error) {
	//: the mounts this process sees.
	return membershipIn(os.ReadFile, dir)
}

// membershipIn is membership over readFile: the cgroup2 mount whose point is
// the longest prefix of dir names the part of the cgroup filesystem it
// exposes, and dir below that point is the same path below that part. A
// directory is a path on this host's mounts; /proc/<pid>/cgroup names the
// cgroup in the hierarchy itself — the two agree only where the hierarchy is
// mounted at "/" of the filesystem, which a cgroup2 mount never is.
func membershipIn(readFile func(string) ([]byte, error), dir string) (string, error) {
	//: a relative or unclean path names no directory unambiguously.
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		//: INVALID_SPEC, with the path.
		return "", notAGroupDir(dir)
	}
	v2, _ := cgroupfs.Mounts(readFile)
	best, below, found := cgroupfs.MountValue{}, "", false
	//: every attachment of the unified hierarchy; the deepest one holding dir.
	for _, mount := range v2 {
		rest, holds := underPoint(mount.Point, dir)
		//: a mount elsewhere, or one a deeper mount already beats.
		if !holds || (found && len(mount.Point) <= len(best.Point)) {
			continue
		}
		best, below, found = mount, rest, true
	}
	//: no cgroup2 mount holds dir.
	if !found {
		//: INVALID_SPEC, with the path.
		return "", notAGroupDir(dir)
	}
	//: the part the mount exposes, then the path below its point.
	return path.Join("/", best.Root, below), nil
}

// underPoint reports whether dir is point or below it, and the path below.
func underPoint(point, dir string) (string, bool) {
	point = filepath.Clean(point)
	//: the mount point itself: the part it exposes.
	if dir == point {
		//: nothing below.
		return "", true
	}
	rest, below := strings.CutPrefix(dir, strings.TrimSuffix(point, "/")+"/")
	//: below the point, or elsewhere.
	return rest, below
}

// notAGroupDir is INVALID_SPEC naming a path no cgroup2 mount holds.
func notAGroupDir(dir string) error {
	//: the central sentinel, with the path.
	return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{}, errs.String("dir", dir))
}
