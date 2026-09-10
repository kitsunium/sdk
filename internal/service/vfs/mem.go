// Package vfs — the in-memory filesystem, and the read half of its contract.
package vfs

import (
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// rootName is the one name that always exists in an in-memory filesystem.
const rootName string = "."

// rootPerm is the mode the implicit root directory reports. It is never
// applied to anything on a device — nothing here touches one — and exists so
// fs.WalkDir sees a plausible directory at the top of the tree.
const rootPerm fs.FileMode = 0o755

// memFS is a filesystem held entirely in a map, for the code that would
// otherwise need a temporary directory to be testable.
//
// It answers the SAME typed refusals as the disk filesystem, which is the only
// thing that makes it a double rather than a different filesystem that happens
// to have the same method names. Three differences are real and are stated
// rather than hidden:
//
//   - There are no symbolic links, so [corevfs.PathEscaped] is unreachable
//     here. Confinement is total instead: a name is a map key, and a map key
//     cannot address anything the map does not hold.
//   - ModTime is the zero time — see [memInfo].
//   - WriteAtomic is atomic with respect to readers of this filesystem and
//     makes no durability claim, because there is no device to make one about.
type memFS struct {
	// mu guards nodes. It is an RWMutex because reads dominate in the usage
	// this type exists for — a test that writes a fixture once and then walks
	// it — and because, unlike a cache, a read here genuinely does not mutate.
	mu sync.RWMutex
	// nodes maps a clean path to its entry. The root "." is always present.
	nodes map[string]*memNode
}

// NewMem returns an empty in-memory filesystem containing only its root.
//
// It takes no arguments on purpose. Every knob it could offer — a clock, a
// size cap, a starting tree — would be a knob a consumer's test has to set
// before it can assert anything, and the value of this type is that a test
// double costs one line.
func NewMem() corevfs.FullFS {
	//: the root exists from the start, so an empty filesystem is still
	//: walkable and fs.WalkDir(fsys, ".") does not fail on line one.
	return &memFS{nodes: map[string]*memNode{
		rootName: {mode: fs.ModeDir | rootPerm},
	}}
}

// Open resolves name and returns it as an fs.File.
func (m *memFS) Open(name string) (file fs.File, err error) {
	//: the same lexical grammar the disk filesystem applies.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, found := m.nodes[name]
	//: an absent name gets the *fs.PathError io/fs consumers already match on.
	if !found {
		//: ReadFailed, wrapping fs.ErrNotExist.
		return nil, failRead(notExist("open", name), kerrs.String("path", name))
	}
	//: a directory hands back a handle carrying its listing, so fs.WalkDir
	//: works from Open alone.
	if node.mode.IsDir() {
		//: sorted at capture time, as io/fs requires.
		return &memDir{info: infoOf(name, node), entries: m.entriesLocked(name)}, nil
	}
	//: a file hands back a private COPY, so a later write cannot race a reader.
	return &memFile{info: infoOf(name, node), data: slices.Clone(node.data)}, nil
}

// Stat implements fs.StatFS.
func (m *memFS) Stat(name string) (info fs.FileInfo, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, found := m.nodes[name]
	//: ReadFailed, wrapping fs.ErrNotExist.
	if !found {
		//: nothing partial escapes.
		return nil, failRead(notExist("stat", name), kerrs.String("path", name))
	}
	//: a value type, so the caller cannot reach the node through it.
	return infoOf(name, node), nil
}

// ReadDir implements fs.ReadDirFS, sorted by filename as io/fs requires.
func (m *memFS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, found := m.nodes[name]
	//: ReadFailed, wrapping fs.ErrNotExist.
	if !found {
		//: absent.
		return nil, failRead(notExist("readdir", name), kerrs.String("path", name))
	}
	//: listing a file is a caller mistake, not an empty directory.
	if !node.mode.IsDir() {
		//: NotRegularFile reads backwards here, so the honest answer is the
		//: same shape the operating system gives: an invalid argument.
		return nil, failRead(&fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid},
			kerrs.String("path", name))
	}
	//: one level, sorted.
	return m.entriesLocked(name), nil
}

// ReadFile implements fs.ReadFileFS.
func (m *memFS) ReadFile(name string) (content []byte, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, found := m.nodes[name]
	//: ReadFailed, wrapping fs.ErrNotExist.
	if !found {
		//: absent.
		return nil, failRead(notExist("open", name), kerrs.String("path", name))
	}
	//: reading a directory as bytes is the same refusal ReadDir gives a file.
	if node.mode.IsDir() {
		//: ReadFailed, wrapping fs.ErrInvalid.
		return nil, failRead(&fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid},
			kerrs.String("path", name))
	}
	//: a copy — the caller must not be able to edit the filesystem by
	//: appending to the slice it was handed.
	return slices.Clone(node.data), nil
}

// entriesLocked lists the DIRECT children of dir, sorted by name. The caller
// MUST hold at least the read lock.
func (m *memFS) entriesLocked(dir string) []fs.DirEntry {
	names := make([]string, 0, len(m.nodes))
	//: a flat map plus a prefix test is O(N) per listing, which is the right
	//: trade for a type whose trees are fixtures: it keeps every mutation a
	//: single map assignment, and there is no parent-child index to leave
	//: inconsistent after a RemoveAll.
	for candidate := range maps.Keys(m.nodes) {
		//: a DIRECT child only — path.Dir equality excludes deeper
		//: descendants, and the root is never an entry of itself.
		if path.Dir(candidate) == dir && candidate != rootName {
			names = append(names, candidate)
		}
	}
	slices.Sort(names)
	entries := make([]fs.DirEntry, 0, len(names))
	//: built from the SORTED names, so the listing io/fs requires to be
	//: ordered is ordered by construction rather than by a later sort.
	for _, name := range names {
		entries = append(entries, fs.FileInfoToDirEntry(infoOf(name, m.nodes[name])))
	}
	//: sorted by full path, which for one directory is sorted by base name.
	return entries
}

// infoOf builds the fs.FileInfo for one node.
func infoOf(name string, node *memNode) memInfo {
	//: fs.FileInfo.Name is the BASE name; handing back the full path breaks
	//: fs.WalkDir, which joins it with the directory it came from.
	return memInfo{name: path.Base(name), size: int64(len(node.data)), mode: node.mode}
}

// notExist builds the *fs.PathError an absent name produces, so the in-memory
// filesystem answers errors.Is(err, fs.ErrNotExist) exactly as the disk one
// does. That single sentence is most of what makes it a usable double.
func notExist(op, name string) error {
	//: a struct literal of a stdlib type, not a minted error string.
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
}

// dirExistsLocked reports whether dir is present and is a directory. The caller
// MUST hold at least the read lock.
func (m *memFS) dirExistsLocked(dir string) bool {
	node, found := m.nodes[dir]
	//: a parent that is a FILE is not a parent.
	return found && node.mode.IsDir()
}

// blockedAncestorLocked reports the first component of name's PARENT chain
// that exists and is not a directory. It is the in-memory twin of
// osFS.blockedAncestor, and exists so both filesystems name the blocker rather
// than reporting a bare "no such directory". The caller MUST hold at least the
// read lock.
func (m *memFS) blockedAncestorLocked(name string) (blocker string, blocked bool) {
	parts := strings.Split(path.Dir(name), "/")
	//: walked from the TOP down, so the blocker reported is the shallowest
	//: one — the component the caller has to deal with first.
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		//: a top-level target has no ancestor chain to walk.
		if prefix == rootName {
			//: nothing above it.
			continue
		}
		existing, found := m.nodes[prefix]
		//: a component that is absent is not the blocker.
		if !found {
			//: keep walking.
			continue
		}
		//: the first non-directory in the chain is the reportable cause.
		if !existing.mode.IsDir() {
			//: found it.
			return prefix, true
		}
	}
	//: the chain is clear.
	return "", false
}

// hasChildrenLocked reports whether dir still holds an entry. The caller MUST
// hold at least the read lock.
func (m *memFS) hasChildrenLocked(dir string) bool {
	prefix := dir + "/"
	//: a flat map has no child index, so emptiness is a scan; it stops at the
	//: first descendant found rather than counting them.
	for candidate := range maps.Keys(m.nodes) {
		//: any descendant at any depth counts; Remove refuses either way.
		if strings.HasPrefix(candidate, prefix) {
			//: not empty.
			return true
		}
	}
	//: empty.
	return false
}
