package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// windowBarrierWait is how long a probed flush waits for the rest of its
// window to enter theirs. It is a bound on a test that fails, never a pause
// of one that passes: a window whose flushes run at once releases every one
// of them as soon as its last arrives. Flushes run one after the other wait
// it out once, against a deadline the whole batch shares, and the count of
// flushes in flight then says 1.
const windowBarrierWait time.Duration = 3 * time.Second

// windowProbe watches a batch's mechanics (#827): how many temporaries are
// open at once, how many file flushes are in flight at once, the renames in
// the order they ran, and one file whose flush fails. Each flush waits at a
// barrier until every flush of its window has entered, so the count in
// flight reaches a window's size only if the window's flushes really overlap.
type windowProbe struct {
	native   atomicOps
	failSync int
	size     int
	deadline time.Time

	mu       sync.Mutex
	created  int
	open     int
	maxOpen  int
	inFlight int
	maxSync  int
	entered  map[int]int
	release  map[int]chan struct{}
	renamed  []string
}

// newWindowProbe probes a batch of size files over disk's own mechanics,
// failing the flush of file failSync (none when it is negative).
func newWindowProbe(disk *osFS, size, failSync int) *windowProbe {
	return &windowProbe{
		native:   disk.nativeOps(),
		failSync: failSync,
		size:     size,
		deadline: time.Now().Add(windowBarrierWait),
		entered:  map[int]int{},
		release:  map[int]chan struct{}{},
	}
}

// ops returns the probed mechanics.
func (p *windowProbe) ops() atomicOps {
	probed := p.native
	probed.create = func(name string, perm fs.FileMode) (tempFile, error) {
		file, err := p.native.create(name, perm)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		index := p.created
		p.created++
		p.open++
		p.maxOpen = max(p.maxOpen, p.open)
		return &probedTemp{tempFile: file, probe: p, index: index}, nil
	}
	probed.rename = func(from, to string) error {
		p.mu.Lock()
		p.renamed = append(p.renamed, to)
		p.mu.Unlock()
		return p.native.rename(from, to)
	}
	return probed
}

// windowOf returns the window of file index and how many files it holds.
func (p *windowProbe) windowOf(index int) (window, files int) {
	window = index / publishWindow
	return window, min(publishWindow, p.size-window*publishWindow)
}

// enter records a flush of file index entering, and returns the channel
// closed once every flush of its window has.
func (p *windowProbe) enter(index int) <-chan struct{} {
	window, files := p.windowOf(index)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release[window] == nil {
		p.release[window] = make(chan struct{})
	}
	p.entered[window]++
	if p.entered[window] == files {
		close(p.release[window])
	}
	p.inFlight++
	p.maxSync = max(p.maxSync, p.inFlight)
	return p.release[window]
}

// leave records a flush leaving.
func (p *windowProbe) leave() {
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
}

// closed records a temporary closed.
func (p *windowProbe) closed() {
	p.mu.Lock()
	p.open--
	p.mu.Unlock()
}

// probedTemp is one temporary of a probed batch.
type probedTemp struct {
	tempFile
	probe *windowProbe
	index int
}

// Sync waits at its window's barrier, then flushes or fails.
func (t *probedTemp) Sync() error {
	release := t.probe.enter(t.index)
	timer := time.NewTimer(time.Until(t.probe.deadline))
	defer timer.Stop()
	select {
	case <-release:
	case <-timer.C:
	}
	defer t.probe.leave()
	if t.index == t.probe.failSync {
		return errInjected
	}
	return t.tempFile.Sync()
}

// Close records the descriptor released.
func (t *probedTemp) Close() error {
	t.probe.closed()
	return t.tempFile.Close()
}

// TestABatchFlushesAWindowAtOnceAndRenamesInOrder is the design of #827's
// second attempt: the file flushes of a window run at once — a window's
// size of them in flight together, never more temporaries open than a
// window — and the renames follow in the batch's order, one per file.
func TestABatchFlushesAWindowAtOnceAndRenamesInOrder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		files    int
		inFlight int
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		disk, dir := newDiskFS(t)
		probe := newWindowProbe(disk, c.files, -1)
		disk.ops = probe.ops()
		batch := batchOf(".", c.files)

		published, err := disk.WriteAtomicAll(batch, 0o600)

		if published != c.files || err != nil {
			t.Fatalf("WriteAtomicAll(%d) = %d, %v, want %d, nil", c.files, published, err, c.files)
		}
		if probe.maxSync != c.inFlight {
			t.Errorf("%d file flushes were in flight at once, want %d — a window's flushes run together", probe.maxSync, c.inFlight)
		}
		if probe.maxOpen > publishWindow {
			t.Errorf("%d temporaries were open at once, want at most %d", probe.maxOpen, publishWindow)
		}
		if probe.open != 0 {
			t.Errorf("%d temporaries left open", probe.open)
		}
		if len(probe.renamed) != c.files {
			t.Fatalf("%d renames, want %d", len(probe.renamed), c.files)
		}
		for index, name := range probe.renamed {
			if name != batch[index].Name {
				t.Fatalf("rename %d was %s, want %s — the renames run in the batch's order", index, name, batch[index].Name)
			}
		}
		if orphans := leftoverTemps(t, dir); len(orphans) != 0 {
			t.Errorf("temporaries left behind: %v", orphans)
		}
	}
	for _, c := range []tc{
		{name: "one", files: 1, inFlight: 1},
		{name: "three", files: 3, inFlight: 3},
		{name: "a window", files: publishWindow, inFlight: publishWindow},
		{name: "two windows and a part", files: 2*publishWindow + 3, inFlight: publishWindow},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestABatchWhoseKthFlushFailsPublishesTheFirstKAndNothingAfter is the
// prefix semantics ADR 0056 kept when the flushes went concurrent (amended
// 2026-11-15): the flush of file k fails while every other flush of the
// batch, later ones of its window included, succeeds. The count is k, the
// error PUBLISH_FAILED for file k with the cause kept, the renames are
// exactly files 0 to k−1 in order — none after k —, the first k hold their
// bytes behind one directory flush, the rest were never created, and no
// temporary survives.
func TestABatchWhoseKthFlushFailsPublishesTheFirstKAndNothingAfter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		fail int
	}
	const files = 2*publishWindow + 3
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		disk, dir := newDiskFS(t)
		probe := newWindowProbe(disk, files, c.fail)
		flushed, dirs := 0, 0
		probed := probe.ops()
		probed.syncDir = func(name string) error {
			dirs++
			return probe.native.syncDir(name)
		}
		disk.ops = probed
		batch := batchOf(".", files)

		published, err := disk.WriteAtomicAll(batch, 0o600)

		if published != c.fail || !kerrs.HasCode(err, corevfs.CodePublishFailed) || !errors.Is(err, errInjected) {
			t.Fatalf("WriteAtomicAll = %d, %v, want %d and PUBLISH_FAILED over the injected cause", published, err, c.fail)
		}
		if named := pathOf(err); named != batch[c.fail].Name {
			t.Errorf("the failure names %q, want file %d, %s", named, c.fail, batch[c.fail].Name)
		}
		if len(probe.renamed) != c.fail {
			t.Fatalf("%d renames, want %d — none at or after the failed file", len(probe.renamed), c.fail)
		}
		for index, name := range probe.renamed {
			if name != batch[index].Name {
				t.Fatalf("rename %d was %s, want %s", index, name, batch[index].Name)
			}
		}
		if want := min(c.fail, 1); dirs != want {
			t.Errorf("%d directory flushes, want %d", dirs, want)
		}
		for index, file := range batch {
			got, readErr := os.ReadFile(filepath.Join(dir, file.Name))
			if index < c.fail {
				if readErr != nil || string(got) != string(file.Data) {
					t.Errorf("file %d = %q, %v, want its new bytes", index, got, readErr)
				}
				flushed++
				continue
			}
			if !errors.Is(readErr, fs.ErrNotExist) {
				t.Errorf("file %d exists (%q, %v), want it unpublished", index, got, readErr)
			}
		}
		if probe.open != 0 {
			t.Errorf("%d temporaries left open", probe.open)
		}
		if orphans := leftoverTemps(t, dir); len(orphans) != 0 {
			t.Errorf("temporaries left behind: %v", orphans)
		}
		if flushed != c.fail {
			t.Errorf("%d files published, want %d", flushed, c.fail)
		}
	}
	for _, c := range []tc{
		{name: "the first", fail: 0},
		{name: "inside the first window", fail: 5},
		{name: "the last of the first window", fail: publishWindow - 1},
		{name: "the first of the second window", fail: publishWindow},
		{name: "the last of the batch", fail: files - 1},
	} {
		t.Run(c.name+"/"+strconv.Itoa(c.fail), func(t *testing.T) { runCase(t, c) })
	}
}

// pathOf returns the last "path" field err carries, or "" without one.
func pathOf(err error) string {
	named := ""
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == "path" {
			named = field.StringValue()
		}
	}
	return named
}
