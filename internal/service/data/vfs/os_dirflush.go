package vfs

import (
	"context"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/concur/commit"
)

// dirFlushes routes step 5 of every publication — the flush of the directory
// naming the file — through one commit group per directory, so publications
// into one directory that reach step 5 while a flush of it runs share the
// next flush instead of each paying its own (#803, ADR 0207).
//
// The guarantee each publication needs is unchanged: a directory flush that
// BEGINS after its rename. A publication joins the batch still gathering —
// never one already flushing, which the group has sealed — and that batch's
// flush begins only after every one of its publications has joined, so after
// each of their renames. Every participant receives that flush's verdict.
//
// A directory's group lives while a publication waits on it and is dropped
// with the last one, so the map holds the directories in use and no others;
// the entry dropped is kept as the next directory's (spare), so a publication
// that finds its directory idle builds no group (#813). Alone on its
// directory, it then flushes on its own goroutine through the group's idle
// path — the same fsync, begun after its rename, with no batch built.
type dirFlushes struct {
	// flush is the mechanic one batch runs: the directory's fsync.
	flush func(dir string) error
	// groups holds one group per directory a publication is waiting on.
	groups map[string]*dirFlush
	// spare is the entry the last directory to fall idle left, rebound to
	// the next directory a publication finds idle; nil while none is.
	spare *dirFlush
	mu    sync.Mutex
}

// dirFlush is one directory's group, the directory it flushes and how many
// publications wait on it.
type dirFlush struct {
	group *commit.Group[struct{}]
	// dir is set under dirFlushes.mu when the entry is bound to a directory,
	// before any Do on group, and read by the group's flush: a spare is
	// rebound only once every publication through it has had its verdict.
	dir     string
	waiting int
}

// newDirFlushes shares flush between the publications into one directory.
func newDirFlushes(flush func(dir string) error) *dirFlushes {
	//: no directory is in use yet.
	return &dirFlushes{flush: flush, groups: map[string]*dirFlush{}}
}

// sync flushes dir for one publication whose rename has returned, sharing the
// flush with the publications into dir that join the same batch, and returns
// that flush's error.
func (d *dirFlushes) sync(dir string) error {
	entry := d.join(dir)
	//: the directory's group is dropped with its last publication, a panic
	//: re-raised by the group included.
	defer d.leave(dir, entry)
	//: no deadline: a publication waits for its flush however long it takes,
	//: as a direct fsync would, so it never leaves its batch early.
	return entry.group.Do(context.Background(), struct{}{})
}

// join returns dir's group, creating it when no publication waits on it,
// with this publication counted.
func (d *dirFlushes) join(dir string) *dirFlush {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, open := d.groups[dir]
	//: the first publication into dir since the last one left.
	if !open {
		entry = d.bind(dir)
		d.groups[dir] = entry
	}
	entry.waiting++
	//: the group this publication waits on.
	return entry
}

// bind returns an idle entry for dir: the spare when one is kept, else a new
// entry and its group. d.mu is held.
func (d *dirFlushes) bind(dir string) *dirFlush {
	entry := d.spare
	d.spare = nil
	//: none kept: the first directory, or several in use at once.
	if entry == nil {
		entry = &dirFlush{}
		entry.group = commit.New(func(context.Context, []struct{}) error {
			//: one fsync for the whole batch, of the directory bound now.
			return d.flush(entry.dir)
		}, commit.Config{})
	}
	entry.dir = dir
	//: idle: no publication waits on it, no flush of it runs.
	return entry
}

// leave uncounts one publication and drops dir's group with the last one: no
// batch of it is left, since every participant has had its verdict.
func (d *dirFlushes) leave(dir string, entry *dirFlush) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry.waiting--
	//: the last publication into dir: nothing will flush through it again.
	if entry.waiting == 0 {
		delete(d.groups, dir)
		d.spare = entry
	}
}
