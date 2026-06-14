//go:build linux

// Package cgroup — Linux cgroup v2 control-group implementation.
package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// controllersFile is the cgroup v2 marker: its presence under the mount proves
// the unified hierarchy is in use (cgroup v1 has no such file at the root).
const controllersFile string = "cgroup.controllers"

// procsFile is the controller file that attaches a process to a group when its
// pid is written, one pid per write (cgroup.procs).
const procsFile string = "cgroup.procs"

// unlimited is the literal cgroup v2 writes for "no limit" on the numeric
// controller files (memory.max, pids.max, and the quota field of cpu.max).
const unlimited string = "max"

// controllerPerm is the mode for a freshly created control-group directory;
// the kernel ignores the bits but mkdir requires a mode argument.
const controllerPerm os.FileMode = 0o755

// decimalBase is the radix used when rendering pids and limit values.
const decimalBase int = 10

// exitUnavailable is sysexits.h EX_UNAVAILABLE (69), the exit status the
// CGROUP_UNAVAILABLE sentinel carries; restated to wrap a cause with matching
// semantics without re-Defining the central code.
const exitUnavailable int = 69

// exitOSErr is sysexits.h EX_OSERR (71), the exit status the cgroup create /
// write / delete sentinels carry; restated for the same reason.
const exitOSErr int = 71

// exitUsage is sysexits.h EX_USAGE (64), the exit status the INVALID_SPEC
// sentinel carries; restated to wrap a rejected name without re-Defining the
// central code.
const exitUsage int = 64

// controlGroup is a handle to one cgroup v2 control group: its on-disk
// directory under the unified hierarchy. It satisfies coreproc.Group; all
// methods write the matching controller interface file or rmdir the directory.
type controlGroup struct {
	// dir is the absolute path of the control-group directory.
	dir string
}

// available reports whether the unified cgroup v2 hierarchy is mounted and a
// sub-group can actually be created under it by the caller. It probes for the
// cgroup.controllers marker, then confirms write access by creating and
// removing a throwaway directory — the only honest test of delegation.
func available() bool {
	//: the controllers marker is absent on non-v2 and unmounted hosts.
	if _, err := os.Stat(filepath.Join(mountRoot, controllersFile)); err != nil {
		//: no unified hierarchy here.
		return false
	}
	//: a unique temp name is the only reliable delegation test that never
	//: false-negatives on a leftover probe directory; a read-only root rejects it.
	probe, err := os.MkdirTemp(mountRoot, ".sdk-cgroup-probe-")
	if err != nil {
		//: not delegated/writable to the caller.
		return false
	}
	//: best-effort cleanup of the probe directory; a leaked probe is harmless.
	if rerr := os.Remove(probe); rerr != nil {
		//: the directory was created, so the caller can still create groups.
		return true
	}
	//: mounted, v2, and writable — the caller may create groups.
	return true
}

// cgroupUnavailable wraps cause in the central CGROUP_UNAVAILABLE sentinel,
// annotated with the offending path. It restates the sentinel's fields verbatim;
// the code is never re-Defined here.
func cgroupUnavailable(cause error, pathKey, pathVal string) error {
	//: restate the central CGROUP_UNAVAILABLE fields; never re-Define.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupUnavailable,
		Reason:   "CGROUP_UNAVAILABLE",
		Public:   "cgroup v2 is not available or not delegated",
		Private:  "service/proc/cgroup: the unified cgroup v2 hierarchy is absent or not writable by the caller",
		ExitCode: exitUnavailable,
	}, errs.String(pathKey, pathVal))
}

// createGroup makes a new control-group directory named name under the
// configured root and returns a controlGroup bound to it. It maps an absent
// hierarchy to CgroupUnavailable and a failed mkdir to CgroupCreateFailed.
func createGroup(name string, opts ...Option) (g coreproc.Group, err error) {
	cfg := applyOptions(opts)
	root := filepath.Clean(cfg.root)
	//: reject a name that is not a single safe element before touching the FS.
	if verr := validateName(name); verr != nil {
		//: a traversing or empty name must never reach filepath.Join.
		return nil, verr
	}
	//: refuse early when the unified hierarchy is absent or not delegated.
	if _, serr := os.Stat(filepath.Join(root, controllersFile)); serr != nil {
		//: an absent controllers marker means there is nothing to delegate.
		return nil, cgroupUnavailable(serr, "root", root)
	}
	dir := filepath.Join(root, name)
	//: defence in depth — the cleaned join must stay inside the cleaned root.
	if !withinRoot(root, dir) {
		//: a dir that escaped the root is a rejected spec, not a create fault.
		return nil, invalidName(name)
	}
	//: mkdir under the hierarchy materialises the new control group.
	if merr := os.Mkdir(dir, controllerPerm); merr != nil {
		//: a permission/read-only denial here means the root was not delegated.
		return nil, classifyCreate(merr, dir)
	}
	//: the directory now exists — hand back a bound handle.
	return &controlGroup{dir: dir}, nil
}

// validateName rejects a control-group name that is not a single safe path
// element: empty, ".", "..", or one carrying a path separator or traversal
// segment could resolve outside the delegated root, so it returns the bare
// INVALID_SPEC sentinel rather than letting filepath.Join escape the subtree.
func validateName(name string) error {
	//: empty, dot-aliases, separators, and any non-canonical form can escape or
	//: alias the parent, so reject anything that is not a single literal element.
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, os.PathSeparator) ||
		filepath.Clean(name) != name {
		//: nothing here is a safe single element — reject before the FS sees it.
		return invalidName(name)
	}
	//: the name is a single safe element.
	return nil
}

// withinRoot reports whether dir, already cleaned, is root itself or a strict
// descendant of root. It guards the post-join path so a crafted name can never
// land a control group outside the delegated subtree.
func withinRoot(root, dir string) bool {
	//: the root itself is never a valid leaf group directory.
	if dir == root {
		//: a name that collapsed to the root escaped the intended subtree.
		return false
	}
	//: a strict descendant carries the root plus a separator as its prefix.
	return strings.HasPrefix(dir, root+string(os.PathSeparator))
}

// invalidName wraps the central INVALID_SPEC sentinel annotated with the
// offending name. It restates the sentinel's fields verbatim; the code is never
// re-Defined here.
func invalidName(name string) error {
	//: restate the central INVALID_SPEC fields; never re-Define the code.
	return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{
		Code:     coreproc.CodeInvalidSpec,
		Reason:   "INVALID_SPEC",
		Public:   "Process specification is invalid",
		Private:  "service/proc/exec.Start: Spec is malformed (empty Path or contradictory attributes)",
		ExitCode: exitUsage,
	}, errs.String("name", name))
}

// notDelegated reports whether a mkdir failure means the cgroup root was not
// delegated to the caller — a permission denial (EACCES/EPERM) or a read-only
// hierarchy (EROFS), both common in unprivileged containers — as opposed to a
// genuine creation fault.
func notDelegated(cause error) bool {
	//: EACCES/EPERM are the classic "no delegation" denials.
	if os.IsPermission(cause) {
		//: a permission denial means the root is not the caller's to write.
		return true
	}
	//: a read-only cgroup mount also means the caller cannot create groups.
	return errors.Is(cause, syscall.EROFS)
}

// classifyCreate maps a mkdir failure to the right sentinel: a delegation denial
// (permission or read-only mount) is reported as CgroupUnavailable, any other
// fault as CgroupCreateFailed.
func classifyCreate(cause error, dir string) error {
	//: a denial that means "not delegated" maps to the unavailable sentinel.
	if notDelegated(cause) {
		//: reuse the shared CGROUP_UNAVAILABLE wrapper for the denial path.
		return cgroupUnavailable(cause, "dir", dir)
	}
	//: restate the central CGROUP_CREATE_FAILED fields; never re-Define.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupCreateFailed,
		Reason:   "CGROUP_CREATE_FAILED",
		Public:   "Could not create the control group",
		Private:  "service/proc/cgroup.Create: mkdir under the cgroup v2 hierarchy failed",
		ExitCode: exitOSErr,
	}, errs.String("dir", dir))
}

// SetMemoryMax writes memory.max; a negative bytes value writes "max" (no
// limit). It returns CgroupWriteFailed when the controller is disabled or the
// value is rejected.
func (g *controlGroup) SetMemoryMax(bytes int64) error {
	//: write the byte count, or "max" to lift the ceiling.
	return g.writeController("memory.max", maxOrValue(bytes))
}

// SetCPUMax writes cpu.max as "quota period"; a negative quota writes "max
// <period>" (no CPU cap). It returns CgroupWriteFailed on a rejected value.
func (g *controlGroup) SetCPUMax(quota, period int64) error {
	value := maxOrValue(quota) + " " + strconv.FormatInt(period, decimalBase)
	//: cpu.max takes a "quota period" pair; "max" lifts the quota.
	return g.writeController("cpu.max", value)
}

// SetPidsMax writes pids.max; a negative n writes "max" (no process cap). It
// returns CgroupWriteFailed on a rejected value.
func (g *controlGroup) SetPidsMax(n int64) error {
	//: write the process ceiling, or "max" to lift it.
	return g.writeController("pids.max", maxOrValue(n))
}

// SetIOMax writes one io.max line verbatim, e.g. "8:0 rbps=1048576". It returns
// CgroupWriteFailed when the io controller is disabled or the line is malformed.
func (g *controlGroup) SetIOMax(spec string) error {
	//: io.max takes a free-form "MAJ:MIN key=value..." line written as-is.
	return g.writeController("io.max", spec)
}

// Add moves the process pid into this group by writing pid to cgroup.procs. It
// returns CgroupWriteFailed when the pid is invalid or attachment is refused.
func (g *controlGroup) Add(pid int) error {
	//: cgroup.procs takes one pid per write to attach a process.
	return g.writeController(procsFile, strconv.Itoa(pid))
}

// Delete removes the (empty) control-group directory. Callers move processes
// out first; a populated group yields CgroupDeleteFailed (EBUSY).
func (g *controlGroup) Delete() error {
	//: rmdir removes an empty cgroup; a populated one returns EBUSY.
	if err := os.Remove(g.dir); err != nil {
		//: restate the central CGROUP_DELETE_FAILED fields; never re-Define.
		return errs.Wrap(err, errs.WrapParams{
			Code:     coreproc.CodeCgroupDeleteFailed,
			Reason:   "CGROUP_DELETE_FAILED",
			Public:   "Could not delete the control group",
			Private:  "service/proc/cgroup.Delete: rmdir of the control group failed (still populated?)",
			ExitCode: exitOSErr,
		}, errs.String("dir", g.dir))
	}
	//: the control group is gone.
	return nil
}

// writeController writes value to the named controller interface file under the
// group directory, wrapping any failure in CgroupWriteFailed annotated with the
// file name. cgroup v2 interface files take a single write with no trailing
// newline required.
func (g *controlGroup) writeController(name, value string) error {
	path := filepath.Join(g.dir, name)
	//: a single write sets a cgroup v2 interface file; perm is ignored by sysfs.
	if err := os.WriteFile(path, []byte(value), controllerPerm); err != nil {
		//: restate the central CGROUP_WRITE_FAILED fields; never re-Define.
		return errs.Wrap(err, errs.WrapParams{
			Code:     coreproc.CodeCgroupWriteFailed,
			Reason:   "CGROUP_WRITE_FAILED",
			Public:   "Could not write the cgroup controller file",
			Private:  "service/proc/cgroup: writing a controller file failed (controller disabled or value rejected)",
			ExitCode: exitOSErr,
		}, errs.String("file", name))
	}
	//: the controller value is in effect.
	return nil
}

// maxOrValue renders v for a numeric controller file: a negative value becomes
// the literal "max" (no limit), any other value its decimal form.
func maxOrValue(v int64) string {
	//: a negative value is the SDK convention for "no limit".
	if v < 0 {
		//: the kernel spells "no limit" as the literal "max".
		return unlimited
	}
	//: a concrete ceiling is written in decimal.
	return strconv.FormatInt(v, decimalBase)
}
