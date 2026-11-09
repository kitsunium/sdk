package cgroup

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The cgroup v2 files a group's place in the hierarchy is read and written
// through (Documentation/admin-guide/cgroup-v2.rst).
const (
	// subtreeControlFile enables controllers for the groups beneath a group.
	subtreeControlFile string = "cgroup.subtree_control"
	// threadsFile lists the group's threads; delegated with cgroup.procs.
	threadsFile string = "cgroup.threads"
	// delegateList is where the kernel lists the files a delegation hands over
	// (Linux 4.15).
	delegateList string = "/sys/kernel/cgroup/delegate"
	// enablePrefix marks a controller enabled in cgroup.subtree_control.
	enablePrefix string = "+"
)

// documentedDelegation is what a delegation hands over where the kernel has
// no list: cgroup-v2.rst's "Delegation" names the directory and these three.
var documentedDelegation = []string{procsFile, threadsFile, subtreeControlFile}

// inTree runs work on root opened as an os.Root, so that every path work
// resolves stays inside root — a ".." or a link out of it is refused by the
// kernel's openat2 walk — and closes it.
func inTree(root string, work func(tree *os.Root) error) error {
	tree, err := os.OpenRoot(root)
	//: a root that cannot be opened has nothing to make.
	if err != nil {
		//: the caller classifies the open failure.
		return err
	}
	werr := work(tree)
	//: the work's own failure first, then a directory handle that would not close.
	return cmp.Or(werr, tree.Close())
}

// treeMaker makes the groups of one nested name under a root, enabling the
// controllers asked for in each group above the last, and remembers what it
// made so a failure can undo it.
type treeMaker struct {
	// tree is the root, opened as an os.Root.
	tree *os.Root
	// root is the root's path, for the errors; name is the nested name.
	root string
	name string
	// ctrls are enabled from the root down to the last group's parent.
	ctrls []coreproc.Controller
	// made are the groups this call made, outermost first.
	made []string
}

// makeTree enables ctrls in root's cgroup.subtree_control, then makes the
// groups of name top-down — an intermediate one kept when it exists, the last
// one always new — and enables ctrls in each before its child. A group it
// made is removed again when a later step fails.
func makeTree(tree *os.Root, root, name string, ctrls []coreproc.Controller) error {
	m := &treeMaker{tree: tree, root: root, name: name, ctrls: ctrls}
	//: the root's children get the controllers first.
	if err := enableIn(tree.WriteFile, ".", root, ctrls); err != nil {
		//: nothing made yet.
		return err
	}
	//: what a failed step leaves made goes again.
	if err := m.descend(); err != nil {
		m.undo()
		return err
	}
	//: the whole path exists.
	return nil
}

// descend makes each group of the name from the root down, enabling the
// controllers in every group above the last.
func (m *treeMaker) descend() error {
	elements := strings.Split(m.name, string(os.PathSeparator))
	m.made = make([]string, 0, len(elements))
	//: one group per element, from the root down.
	for i := range elements {
		rel := filepath.Join(elements[:i+1]...)
		last := i == len(elements)-1
		//: a group that exists in the last place, a link, a refused mkdir.
		if err := m.step(rel, last); err != nil {
			return err
		}
		//: the next group down gets the controllers from this one; the last
		//: group's are its parent's, already enabled.
		if !last {
			if err := enableIn(m.tree.WriteFile, rel, filepath.Join(m.root, rel), m.ctrls); err != nil {
				return err
			}
		}
	}
	//: every group exists.
	return nil
}

// step makes the group rel. An intermediate group that exists is kept when it
// is a directory — a link is refused, even one inside the root — and the last
// one must be new.
func (m *treeMaker) step(rel string, last bool) error {
	err := m.tree.Mkdir(rel, controllerPerm)
	//: made by this call, to undo on a later failure.
	if err == nil {
		m.made = append(m.made, rel)
		return nil
	}
	//: the last group is never adopted, and only an existing one is kept.
	if last || !errors.Is(err, fs.ErrExist) {
		//: the mkdir's own failure.
		return err
	}
	info, lerr := m.tree.Lstat(rel)
	//: it went between the mkdir and the look.
	if lerr != nil {
		//: the look's failure.
		return lerr
	}
	//: a link or a file stands where a group should.
	if !info.IsDir() {
		//: INVALID_SPEC, naming the element that is not a group.
		return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{},
			errs.String("name", m.name), errs.String("element", rel))
	}
	//: an existing group, kept.
	return nil
}

// undo removes the groups this call made, deepest first. A group that will
// not go is left: it is empty, and removing it is the caller's when it
// retries.
func (m *treeMaker) undo() {
	//: deepest first, so each one is empty when its turn comes.
	for _, rel := range slices.Backward(m.made) {
		//: a failure leaves an empty group behind and nothing else.
		if err := m.tree.Remove(rel); err != nil {
			continue
		}
	}
}

// enableIn writes ctrls as "+a +b" to the cgroup.subtree_control of the group
// rel, through write — os.Root's for a tree being made, the os package's for
// a group's own directory. A controller the kernel refuses there (ENOENT) is
// CgroupControllerAbsent; any other refusal, EBUSY while the group holds
// a process among them, is CgroupWriteFailed.
func enableIn(write func(name string, data []byte, perm os.FileMode) error, rel, dir string, ctrls []coreproc.Controller) error {
	//: nothing asked, nothing written.
	if len(ctrls) == 0 {
		//: a group made without controllers.
		return nil
	}
	line := enableLine(ctrls)
	err := write(filepath.Join(rel, subtreeControlFile), []byte(line), controllerPerm)
	//: the kernel enabled every one.
	if err == nil {
		//: the groups beneath get their files.
		return nil
	}
	//: the kernel names an unknown or unoffered controller with ENOENT.
	if errors.Is(err, fs.ErrNotExist) {
		//: CGROUP_CONTROLLER_ABSENT, with the line and the group.
		return controllerAbsent(err, line, dir)
	}
	//: any other refusal of the write.
	return writeFailed(err, subtreeControlFile, dir)
}

// enableLine renders ctrls as cgroup.subtree_control takes them: "+a +b".
func enableLine(ctrls []coreproc.Controller) string {
	var line strings.Builder
	//: one "+name" per controller, space-separated.
	for i, c := range ctrls {
		//: the separator between two controllers.
		if i > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(enablePrefix)
		line.WriteString(c.String())
	}
	//: the line, written in one write.
	return line.String()
}

// validateControllers refuses a controller no constant holds: its name
// would be "controller(N)", which cgroup.subtree_control would be told.
func validateControllers(ctrls []coreproc.Controller) error {
	//: every controller is checked before anything is written.
	for _, c := range ctrls {
		//: a constant, or a refusal.
		if !c.Known() {
			//: INVALID_SPEC, naming the controller.
			return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{}, errs.String("controller", c.String()))
		}
	}
	//: every controller is one.
	return nil
}

// rootOffers reports, as CgroupControllerAbsent, the first of ctrls the
// cgroup.controllers of dir does not list.
func rootOffers(dir string, ctrls []coreproc.Controller) error {
	//: nothing asked: the marker alone was the gate.
	if len(ctrls) == 0 {
		//: nothing to check.
		return nil
	}
	listed, err := os.ReadFile(filepath.Join(dir, controllersFile))
	//: a group whose offer cannot be read offers nothing.
	if err != nil {
		//: CGROUP_CONTROLLER_ABSENT, with the cause.
		return controllerAbsent(err, enableLine(ctrls), dir)
	}
	offer := strings.TrimSpace(string(listed))
	offered := strings.Fields(offer)
	//: each controller asked for, against the offer.
	for _, c := range ctrls {
		//: a controller the group's parent did not enable for it.
		if !slices.Contains(offered, c.String()) {
			//: CGROUP_CONTROLLER_ABSENT, naming it.
			return errs.Wrap(coreproc.CgroupControllerAbsent, errs.WrapParams{},
				errs.String("controller", c.String()), errs.String("dir", dir), errs.String("offered", offer))
		}
	}
	//: every controller is offered.
	return nil
}

// controllerAbsent wraps cause in the central CGROUP_CONTROLLER_ABSENT
// sentinel, with the controllers asked for and the group.
func controllerAbsent(cause error, controllers, dir string) error {
	//: restate the central fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupControllerAbsent,
		Reason:   "CGROUP_CONTROLLER_ABSENT",
		Public:   "The cgroup controller is not available here",
		Private:  "service/proc/cgroup: the controller is absent from cgroup.controllers or refused by cgroup.subtree_control",
		ExitCode: exitUnavailable,
	}, errs.String("controllers", controllers), errs.String("dir", dir))
}

// writeFailed wraps cause in the central CGROUP_WRITE_FAILED sentinel, with
// the file and the group.
func writeFailed(cause error, file, dir string) error {
	//: restate the central fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupWriteFailed,
		Reason:   "CGROUP_WRITE_FAILED",
		Public:   "Could not write the cgroup controller file",
		Private:  "service/proc/cgroup: writing a controller file failed (controller disabled or value rejected)",
		ExitCode: exitOSErr,
	}, errs.String("file", file), errs.String("dir", dir))
}

// Path is the group's directory: the CgroupPath a Spec spawns a child
// straight into.
func (g *controlGroup) Path() string {
	//: the directory the handle is bound to.
	return g.dir
}

// EnableControllers writes ctrls to the group's cgroup.subtree_control, so
// the groups beneath it get their interface files. A controller the group's
// cgroup.controllers does not list is CgroupControllerAbsent, and a
// malformed name InvalidSpec, before anything is written; while the group
// itself holds a process the kernel refuses with EBUSY, CgroupWriteFailed
// (cgroup v2's no-internal-process rule).
func (g *controlGroup) EnableControllers(ctrls ...coreproc.Controller) error {
	//: a malformed name is refused first.
	if err := validateControllers(ctrls); err != nil {
		//: INVALID_SPEC.
		return err
	}
	//: only what the group is offered can be handed down.
	if err := rootOffers(g.dir, ctrls); err != nil {
		//: CGROUP_CONTROLLER_ABSENT.
		return err
	}
	//: the group's own file, by its path.
	return enableIn(os.WriteFile, g.dir, g.dir, ctrls)
}

// Delegate hands the group to the user uid and the group gid (systemd
// Delegate=yes): it changes the owner of the directory and of the files the
// kernel lists in /sys/kernel/cgroup/delegate — cgroup.procs,
// cgroup.threads and cgroup.subtree_control where that list is absent — so
// that user may create groups beneath it, move its processes and enable
// controllers. -1 keeps an owner unchanged. A file the group lacks is
// skipped; a refused change is CgroupWriteFailed.
func (g *controlGroup) Delegate(uid, gid int) error {
	//: chown(2) reads -1 as "unchanged"; anything below is no id.
	if uid < -1 || gid < -1 {
		//: INVALID_SPEC, with both ids.
		return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{}, errs.Int("uid", uid), errs.Int("gid", gid))
	}
	//: the kernel's list, the directory first; a link is never followed.
	return delegateIn(g.dir, delegationFiles(os.ReadFile), uid, gid, os.Lchown)
}

// delegationFiles reads the files a delegation hands over from the kernel's
// list, keeping plain names only, or returns the three of cgroup-v2.rst's
// "Delegation" where the list is absent (Linux before 4.15).
func delegationFiles(readFile func(string) ([]byte, error)) []string {
	listed, err := readFile(delegateList)
	//: an older kernel: the documented three.
	if err != nil {
		//: cgroup-v2.rst's list, the caller's own copy.
		return slices.Clone(documentedDelegation)
	}
	files := make([]string, 0, len(documentedDelegation))
	//: one file name per line.
	for name := range strings.FieldsSeq(string(listed)) {
		//: a name that is not a file of the group is never chowned.
		if name != "." && name != ".." && filepath.Base(name) == name {
			files = append(files, name)
		}
	}
	//: an empty list hands over the documented three.
	if len(files) == 0 {
		//: cgroup-v2.rst's list, the caller's own copy.
		return slices.Clone(documentedDelegation)
	}
	//: the kernel's list.
	return files
}

// delegateIn changes the owner of dir, then of each of files under it,
// through chown; a file dir lacks is skipped.
func delegateIn(dir string, files []string, uid, gid int, chown func(name string, uid, gid int) error) error {
	//: the directory itself: who may make groups beneath it.
	if err := chown(dir, uid, gid); err != nil {
		//: CGROUP_WRITE_FAILED, naming the directory.
		return writeFailed(err, ".", dir)
	}
	//: each file the delegation hands over.
	for _, name := range files {
		err := chown(filepath.Join(dir, name), uid, gid)
		//: changed, or a file this group has not (a controller not enabled).
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		//: CGROUP_WRITE_FAILED, naming the file.
		return writeFailed(err, name, dir)
	}
	//: the group is the user's.
	return nil
}

// DeleteTree removes the group and every group beneath it, deepest first:
// what a delegated unit made under its group goes with it, as systemd
// cleans a Delegate=yes unit up. A group still holding a process stops it
// with CgroupDeleteFailed naming that group — Kill, and wait for Watch to
// say the group is empty, first. A link is never followed.
func (g *controlGroup) DeleteTree() error {
	//: the group with its subtree.
	return deleteSubtree(g.dir, true)
}

// DeleteBeneath removes every group beneath the group, deepest first; the
// group itself stays. It is proc/service's cleanup of a Delegate=yes unit at
// the end of each run (S0g, #734): the unit's group is its caller's, what the
// service made beneath it goes. A group still holding a process stops it with
// CgroupDeleteFailed naming that group. A link is never followed.
func (g *controlGroup) DeleteBeneath() error {
	//: the subtree, the group kept.
	return deleteSubtree(g.dir, false)
}

// deleteSubtree removes the groups beneath dir, deepest first, and dir
// itself when withRoot is set; a group beneath removed by another hand
// meanwhile is no failure.
func deleteSubtree(dir string, withRoot bool) error {
	groups, err := subtreeOf(dir)
	//: the group itself cannot be listed: it is gone or not the caller's.
	if err != nil {
		//: CGROUP_DELETE_FAILED, naming the group.
		return deleteFailed(err, dir)
	}
	//: subtreeOf lists dir first: kept unless asked for.
	if !withRoot {
		groups = groups[1:]
	}
	//: every child is listed after its parent, so the reverse empties each parent first.
	for _, group := range slices.Backward(groups) {
		rerr := os.Remove(group)
		//: removed, or removed by another hand meanwhile.
		if rerr == nil || (group != dir && errors.Is(rerr, fs.ErrNotExist)) {
			continue
		}
		//: a populated group, or one the caller may not remove.
		return deleteFailed(rerr, group)
	}
	//: the subtree is gone.
	return nil
}

// subtreeOf lists dir and every directory beneath it, each after its parent.
// A link is not a directory here, so none is followed; a group removed during
// the walk is skipped.
func subtreeOf(dir string) ([]string, error) {
	var groups []string
	pending := []string{dir}
	//: breadth first: each group read adds the groups beneath it.
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		entries, err := os.ReadDir(current)
		//: a group beneath that went meanwhile has nothing left to list.
		if err != nil && current != dir && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		//: the group itself cannot be read.
		if err != nil {
			return nil, err
		}
		groups = append(groups, current)
		//: the groups beneath; the interface files and any link are left.
		for _, entry := range entries {
			if entry.IsDir() {
				pending = append(pending, filepath.Join(current, entry.Name()))
			}
		}
	}
	//: the subtree, parents first.
	return groups, nil
}

// deleteFailed wraps cause in the central CGROUP_DELETE_FAILED sentinel, with
// the group that would not go.
func deleteFailed(cause error, dir string) error {
	//: restate the central fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupDeleteFailed,
		Reason:   "CGROUP_DELETE_FAILED",
		Public:   "Could not delete the control group",
		Private:  "service/proc/cgroup.Delete: rmdir of the control group failed (still populated?)",
		ExitCode: exitOSErr,
	}, errs.String("dir", dir))
}
