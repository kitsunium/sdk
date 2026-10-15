package selfupdate

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// companionTarget is one archive member and the installed file it replaces.
type companionTarget struct {
	member string
	path   string
}

// stagedFile is one file of a set on its way in: the staged bytes beside the
// installed file, and what has been done to that file so far.
type stagedFile struct {
	target   string
	tmp      string
	kept     bool
	replaced bool
}

// WithCompanions makes an upgrade also replace each named file the release
// archive carries beside the product binary — a second executable shipped in
// the same archive — from that SAME archive, after the one signature and
// checksum check that covers it: nothing is downloaded twice. A companion is
// replaced where it is installed, beside the running binary, its symlinks
// resolved; one that is not installed there is not created.
//
// Every file is staged in its own directory before any is renamed, and each
// installed one is kept by a hard link as <file>.prev. A rename, or the probe
// WithProbe declared, that fails puts back every file already replaced, so
// the product and its companions are never left at two versions; the kept
// files are dropped once all stand, unless a probe keeps them as it keeps the
// binary's. A companion the archive lacks refuses the upgrade
// (BinaryNotInArchive) with nothing replaced. The replacement of a set
// is never elevated: a directory the user cannot write, or a file system that
// cannot link, refuses it (StagingFailed) with nothing replaced.
func (u *Service) WithCompanions(names ...string) *Service {
	//: A nil Service stays nil, as every other option leaves it.
	if u == nil {
		//: Nothing to configure.
		return nil
	}
	u.companions = slices.Clone(names)
	//: Chainable, like every other option.
	return u
}

// installedCompanions returns the declared companions that are installed
// beside execPath, each at its real path. A companion that is not installed
// is skipped: an upgrade replaces what is there and never creates a file.
func (u *Service) installedCompanions(execPath string) (installed []companionTarget, resolveErr error) {
	dir := filepath.Dir(execPath)
	product := u.innerBinaryName()
	//: Each declared name, matched on its basename only, so no name can reach
	//: outside the directory the binary is installed in.
	for _, name := range u.companions {
		member := u.memberName(filepath.Base(name))
		//: The product itself is replaced by the set already.
		if member == product {
			continue
		}
		resolved, err := u.fs.EvalSymlinks(filepath.Join(dir, member))
		//: Not installed: nothing to replace, and nothing is created.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		//: Installed but unresolvable: replacing it blind could miss the file.
		if err != nil {
			//: Nothing has been written yet.
			return nil, classify(StagingFailed, err,
				errs.String("step", "resolve_companion"),
				errs.String("path", filepath.Join(dir, member)))
		}
		installed = append(installed, companionTarget{member: member, path: resolved})
	}
	//: Possibly empty: the single-binary flow then runs unchanged.
	return installed, nil
}

// memberName is the archive member a companion name stands for on this
// platform: the name itself, with `.exe` on windows like the product binary.
func (u *Service) memberName(name string) string {
	//: Windows executables carry the extension, in the archive as on disk.
	if u.goos == "windows" {
		//: Windows-only branch.
		return name + ".exe"
	}
	//: Linux/Darwin — the bare name.
	return name
}

// replaceSet replaces the binary at execPath and every installed companion
// from the verified archive, as one set: all staged, all kept, then renamed
// in order, and every renamed file put back when a later one, or the probe,
// fails.
func (u *Service) replaceSet(archive []byte, execPath string, companions []companionTarget) error {
	targets := append([]companionTarget{{member: u.innerBinaryName(), path: execPath}}, companions...)
	set, err := u.stageSet(archive, targets)
	//: A member missing from the archive, or a directory that cannot be
	//: written, stops the set before anything is kept or renamed.
	if err != nil {
		//: Bare bubble — stageSet removed what it had staged.
		return err
	}
	err = u.keepSet(set)
	//: Each step runs only once the previous one held.
	if err == nil {
		err = u.renameSet(set)
	}
	//: The probe judges the binary once the whole set stands.
	if err == nil {
		err = u.probeSet(execPath, set)
	}
	//: Whatever failed, no staged file and no needless link is left behind.
	if err != nil {
		u.abandon(set)
		//: Bare bubble — every step classified its own failure.
		return err
	}
	//: Without a probe nothing keeps the previous files, as in the single-binary flow.
	if len(u.probe.args) == 0 {
		u.dropKept(set)
	}
	//: Every file of the set stands.
	return nil
}

// stageSet stages every member of the set before any is renamed. A failure
// removes what was already staged.
func (u *Service) stageSet(archive []byte, targets []companionTarget) ([]stagedFile, error) {
	set := make([]stagedFile, 0, len(targets))
	//: One staged file per target, in the order they will be renamed.
	for _, target := range targets {
		tmp, err := u.stageMember(archive, target)
		//: Nothing has been replaced yet; drop what was staged.
		if err != nil {
			u.abandon(set)
			//: Bare bubble — stageMember classified the step.
			return nil, err
		}
		set = append(set, stagedFile{target: target.path, tmp: tmp})
	}
	//: Every member is staged.
	return set, nil
}

// renameSet renames every staged file over the one it replaces, in order. A
// rename that fails puts back every file already renamed.
func (u *Service) renameSet(set []stagedFile) error {
	//: The rename is the only step that changes what is installed.
	for i := range set {
		//: A failure here leaves earlier files new: put them back.
		if err := u.fs.Rename(set[i].tmp, set[i].target); err != nil {
			rolledBack := u.restore(set)
			//: Name the file and whether the set is back where it was.
			return classify(ReplacementFailed, err,
				errs.String("step", "rename_set"),
				errs.String("path", set[i].target),
				errs.Bool("rolled_back", rolledBack))
		}
		set[i].tmp, set[i].replaced = "", true
	}
	//: Every file is renamed.
	return nil
}

// stageMember writes one archive member to a temporary file in the directory
// of the file it replaces, closed and executable. The set is never elevated,
// so there is no fallback to another directory.
func (u *Service) stageMember(archive []byte, target companionTarget) (tmpPath string, stageErr error) {
	member, cleanup, err := u.openMember(bytes.NewReader(archive), target.member)
	//: A member the archive lacks, or an archive that will not unpack.
	if err != nil {
		//: Origin wins — openMember classified it; the field names the file.
		return "", classify(ArchiveUnreadable, err,
			errs.String("stage", "extract"),
			errs.String("member", target.member))
	}
	defer cleanup()
	tmpFile, err := u.fs.CreateTemp(filepath.Dir(target.path), u.src.Product+"-update-*")
	//: A directory that cannot be written cannot hold a member of the set.
	if err != nil {
		//: Nothing staged for this file.
		return "", classify(StagingFailed, err,
			errs.String("step", "create_temp"),
			errs.String("dir", filepath.Dir(target.path)))
	}
	tmpPath = tmpFile.Name()
	writeErr := u.writeTempFileContent(tmpFile, member)
	closeErr := tmpFile.Close()
	//: A write or a close that failed means the staged bytes are not all there.
	if writeErr != nil || closeErr != nil {
		u.fs.Remove(tmpPath)
		//: Report what failed; a close failure is a write that did not complete.
		return "", classify(StagingFailed, errors.Join(writeErr, closeErr),
			errs.String("step", "write_temp"),
			errs.String("path", target.path))
	}
	//: The member replaces an executable and must stay one.
	if chmodErr := u.fs.Chmod(tmpPath, executablePerm); chmodErr != nil {
		u.fs.Remove(tmpPath)
		//: Still staging: nothing installed has changed.
		return "", classify(StagingFailed, chmodErr,
			errs.String("step", "chmod_temp"),
			errs.String("path", target.path))
	}
	//: Staged and ready to be renamed.
	return tmpPath, nil
}

// keepSet links every installed file of the set to <file>.prev. A set whose
// files cannot all be kept is not replaced at all: without them a failure
// half-way could not be undone.
func (u *Service) keepSet(set []stagedFile) error {
	l, ok := u.fs.(linker)
	//: A file system that cannot link cannot keep anything to put back.
	if !ok {
		//: Refused before any rename.
		return refuse(StagingFailed, errs.String("step", "keep_previous"), errs.Bool("linkable", false))
	}
	//: One link per file, a stale one replaced first.
	for i := range set {
		prev := set[i].target + prevSuffix
		//: A stale <file>.prev that cannot be removed would be put back instead.
		if err := u.fs.Remove(prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
			//: Refused before any rename.
			return classify(StagingFailed, err, errs.String("step", "keep_previous"), errs.String("path", prev))
		}
		//: The link is the copy a rollback renames back.
		if err := l.Link(set[i].target, prev); err != nil {
			//: Refused before any rename.
			return classify(StagingFailed, err, errs.String("step", "keep_previous"), errs.String("path", prev))
		}
		set[i].kept = true
	}
	//: Every installed file is kept.
	return nil
}

// restore puts back every file of the set already replaced, from its kept
// link, and reports whether all of them are back.
func (u *Service) restore(set []stagedFile) bool {
	restored := true
	//: Only a replaced file has anything to put back.
	for i := range set {
		//: Untouched files are already what they were.
		if !set[i].replaced {
			continue
		}
		//: The kept link renamed over the new file restores the old one.
		if err := u.fs.Rename(set[i].target+prevSuffix, set[i].target); err != nil {
			restored = false
			continue
		}
		set[i].replaced, set[i].kept = false, false
	}
	//: True when the set is back where it was.
	return restored
}

// probeSet runs the probe WithProbe declared against the new binary and puts
// the whole set back when it fails. Nothing happens without a probe.
func (u *Service) probeSet(execPath string, set []stagedFile) error {
	//: No probe declared: the renames are the verdict.
	if len(u.probe.args) == 0 {
		//: Nothing to run.
		return nil
	}
	err := u.probeOnce(execPath)
	//: A passing probe lets the set stand.
	if err == nil {
		//: Stands.
		return nil
	}
	rolledBack := u.restore(set)
	//: The same sentinel and fields the single-binary probe reports.
	return classify(ProbeFailed, err, errs.String("path", execPath), errs.Bool("rolled_back", rolledBack),
		errs.String("timeout", u.probe.timeout.String()))
}

// abandon removes whatever a set that did not stand left behind: every staged
// file not renamed, and every kept link no longer needed.
func (u *Service) abandon(set []stagedFile) {
	//: Clean up file by file; a removal that fails leaves debris, never a gap.
	for _, file := range set {
		//: A staged file that was never renamed is debris.
		if file.tmp != "" {
			u.fs.Remove(file.tmp)
		}
		//: A link kept for a file that is (again) the original is not needed.
		if file.kept && !file.replaced {
			u.fs.Remove(file.target + prevSuffix)
		}
	}
}

// dropKept removes the links kept for a set that stands.
func (u *Service) dropKept(set []stagedFile) {
	//: Each kept link has served its purpose.
	for _, file := range set {
		//: Only a kept file has a link.
		if file.kept {
			u.fs.Remove(file.target + prevSuffix)
		}
	}
}
