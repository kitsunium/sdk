// Package vfs — the in-memory filesystem's write half.
package vfs

import (
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// WriteFile writes data to name, creating it with perm or replacing it.
func (m *memFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	//: the two core guards, identical to the disk filesystem's.
	if guardErr := guardNameAndPerm(name, perm); guardErr != nil {
		//: InvalidPath or InvalidPermission.
		return guardErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: everything that could refuse, before anything that mutates.
	if placeErr := m.checkFileTargetLocked(name); placeErr != nil {
		//: NotRegularFile or WriteFailed.
		return placeErr
	}
	//: the write itself is one map assignment.
	m.storeLocked(name, data, perm)
	//: stored.
	return nil
}

// WriteAtomic publishes data at name.
//
// In memory the whole thing is one map assignment taken under the exclusive
// lock, so a concurrent reader observes the previous node or the new one and
// never a partial write. There is no temporary, because there is nothing a
// temporary would protect against: every refusal happens before the single
// mutation, so a failure genuinely cannot leave a half-written entry.
//
// It makes NO durability claim. There is no device, and an in-memory
// filesystem that talked about surviving a crash would be exactly the
// "filesystem that pretends" this domain refuses to ship.
func (m *memFS) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	//: the same guards, so a caller that swaps NewOS for NewMem in a test is
	//: exercising the same refusals.
	if guardErr := guardNameAndPerm(name, perm); guardErr != nil {
		//: InvalidPath or InvalidPermission.
		return guardErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: check everything first — that IS the atomicity here.
	if placeErr := m.checkFileTargetLocked(name); placeErr != nil {
		//: NotRegularFile or WriteFailed; nothing was touched.
		return placeErr
	}
	//: one assignment, under the exclusive lock.
	m.storeLocked(name, data, perm)
	//: published.
	return nil
}

// checkFileTargetLocked rejects every reason a file write cannot proceed. The
// caller MUST hold the write lock.
func (m *memFS) checkFileTargetLocked(name string) error {
	//: a FILE where a directory has to be gets the same named verdict the
	//: disk filesystem produces, rather than a bare ENOTDIR — the two must
	//: answer identically or this is not a double.
	if blocker, blocked := m.blockedAncestorLocked(name); blocked {
		//: NotRegularFile, naming the component rather than the request.
		return verdict(corevfs.NotRegularFile, kerrs.String("path", blocker))
	}
	//: a missing parent is NOT created — the port says so, and the disk
	//: filesystem gets the same ENOENT from the kernel.
	if !m.dirExistsLocked(path.Dir(name)) {
		//: WriteFailed, wrapping fs.ErrNotExist.
		return failWrite(notExist("open", path.Dir(name)), kerrs.String("path", name))
	}
	existing, found := m.nodes[name]
	//: a fresh name is always writable.
	if !found {
		//: clear.
		return nil
	}
	//: a directory in the way is the one occupant a file write refuses.
	if existing.mode.IsDir() {
		//: NotRegularFile.
		return verdict(corevfs.NotRegularFile, kerrs.String("path", name))
	}
	//: an existing regular file is replaced.
	return nil
}

// storeLocked performs the mutation. The caller MUST hold the write lock and
// MUST have run [memFS.checkFileTargetLocked] first.
func (m *memFS) storeLocked(name string, data []byte, perm fs.FileMode) {
	mode := perm
	//: perm applies on CREATION only, which is os.WriteFile's rule — so a
	//: rewrite through either filesystem leaves the mode alone.
	if existing, found := m.nodes[name]; found {
		mode = existing.mode
	}
	//: clone, so the caller cannot mutate stored content through its slice.
	m.nodes[name] = &memNode{mode: mode, data: slices.Clone(data)}
}

// MkdirAll creates name and every missing parent.
func (m *memFS) MkdirAll(name string, perm fs.FileMode) error {
	//: the two core guards.
	if guardErr := guardNameAndPerm(name, perm); guardErr != nil {
		//: InvalidPath or InvalidPermission.
		return guardErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parts := strings.Split(name, "/")
	//: refuse the whole chain before creating any of it, so a MkdirAll that
	//: fails halfway does not leave half a tree behind — the disk filesystem
	//: cannot promise that, and this one can, so it does.
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		existing, found := m.nodes[prefix]
		//: a component that exists and is not a directory blocks the chain.
		if found && !existing.mode.IsDir() {
			//: NotRegularFile, naming the component rather than the request.
			return verdict(corevfs.NotRegularFile, kerrs.String("path", prefix))
		}
	}
	//: nothing can refuse now.
	m.createChainLocked(parts, perm)
	//: the whole chain exists.
	return nil
}

// createChainLocked adds every missing component. The caller MUST hold the
// write lock and MUST have verified the chain.
func (m *memFS) createChainLocked(parts []string, perm fs.FileMode) {
	//: top-down, so every parent exists before the child that needs it.
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		//: an existing directory is idempotent success, mode untouched.
		if _, found := m.nodes[prefix]; !found {
			m.nodes[prefix] = &memNode{mode: fs.ModeDir | perm}
		}
	}
}

// Remove removes one file or one empty directory.
func (m *memFS) Remove(name string) error {
	//: no mode is involved, so only the path grammar applies.
	if pathErr := corevfs.ValidateWritePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, found := m.nodes[name]
	//: an absent name is an error here and success in RemoveAll — os.Remove's
	//: asymmetry, kept.
	if !found {
		//: WriteFailed, wrapping fs.ErrNotExist.
		return failWrite(notExist("remove", name), kerrs.String("path", name))
	}
	//: a populated directory is refused rather than emptied.
	if node.mode.IsDir() && m.hasChildrenLocked(name) {
		//: DirectoryNotEmpty — RemoveAll is the call that was wanted.
		return verdict(corevfs.DirectoryNotEmpty, kerrs.String("path", name))
	}
	delete(m.nodes, name)
	//: gone.
	return nil
}

// RemoveAll removes name and everything beneath it. An absent name is success.
func (m *memFS) RemoveAll(name string) error {
	//: the write grammar refuses ".", which here would empty the map.
	if pathErr := corevfs.ValidateWritePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := name + "/"
	//: collect first, delete after — deleting while ranging a map is legal in
	//: Go but reads as a bug to everyone who has to review it.
	doomed := make([]string, 0, len(m.nodes))
	//: one pass over the flat map finds the target and every descendant at
	//: any depth, which is what makes this recursive without a recursion.
	for candidate := range maps.Keys(m.nodes) {
		//: the name itself, or anything beneath it.
		if candidate == name || strings.HasPrefix(candidate, prefix) {
			doomed = append(doomed, candidate)
		}
	}
	//: the deletion pass, now that the map is no longer being ranged.
	for _, candidate := range doomed {
		delete(m.nodes, candidate)
	}
	//: idempotent: nothing to delete is the same answer as deleting it.
	return nil
}
