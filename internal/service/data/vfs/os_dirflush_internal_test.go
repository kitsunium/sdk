package vfs

import (
	"errors"
	"io/fs"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// parkedInGroups reports how many goroutines are parked in a commit group's
// Do, waiting for a verdict — that is, have joined a batch. Do joins under the
// group's lock before it parks, so a parked caller is in a batch.
func parkedInGroups() int {
	buf := make([]byte, 1<<20)
	dump := string(buf[:runtime.Stack(buf, true)])
	parked := 0
	for _, goroutine := range strings.Split(dump, "\n\n") {
		//: a goroutine blocked in Do's select: its header says "[select" and
		//: Do is its top frame — the traceback elides the runtime's.
		header, frames, _ := strings.Cut(goroutine, "\n")
		if strings.Contains(header, "[select") && strings.HasPrefix(frames, "github.com/kitsunium/sdk/internal/kernel/concur/commit.(*Group[...]).Do(") {
			parked++
		}
	}
	return parked
}

// eventually fails t when cond has not held within ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("after ten seconds, still not: %s", what)
		}
		runtime.Gosched()
	}
}

// groupsInUse counts the directories d holds a group for.
func groupsInUse(d *dirFlushes) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.groups)
}

// TestPublicationsIntoOneDirectoryShareTheNextFlush pins #803's adoption: the
// publications that reach step 5 while a flush of their directory runs share
// ONE flush after it, rather than paying one each; and the directory's group
// is gone once the last of them has its verdict.
//
// It does not run in parallel: it counts the goroutines parked in a commit
// group, and a parallel test's would be counted too.
func TestPublicationsIntoOneDirectoryShareTheNextFlush(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	flushes := newDirFlushes(func(dir string) error {
		if dir != "queue/ready" {
			t.Errorf("flushed %q, want queue/ready", dir)
		}
		//: the first flush holds until every follower has joined the next batch.
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	const followers = 7
	verdicts := make([]error, followers+1)
	var wg sync.WaitGroup
	wg.Go(func() { verdicts[0] = flushes.sync("queue/ready") })
	<-started
	for i := 1; i <= followers; i++ {
		wg.Go(func() { verdicts[i] = flushes.sync("queue/ready") })
	}
	eventually(t, "the leader and its seven followers parked in the group", func() bool { return parkedInGroups() >= followers+1 })
	close(release)
	wg.Wait()
	for i, err := range verdicts {
		if err != nil {
			t.Errorf("publication %d: %v", i, err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d publications into one directory ran %d flushes, want 2: the running one and one shared by the seven that arrived during it", followers+1, n)
	}
	if n := groupsInUse(flushes); n != 0 {
		t.Fatalf("%d directory groups held after every publication returned, want none", n)
	}
}

// TestEveryPublicationsFlushBeginsAfterItsRename pins the guarantee step 5
// exists for, under sharing: for every publication, some directory flush
// BEGAN after its rename and ENDED before its verdict. A flush already running
// when a publication arrives never counts for it.
func TestEveryPublicationsFlushBeginsAfterItsRename(t *testing.T) {
	t.Parallel()
	type span struct{ began, ended int64 }
	var (
		tick  atomic.Int64
		mu    sync.Mutex
		spans []span
	)
	flushes := newDirFlushes(func(string) error {
		began := tick.Add(1)
		runtime.Gosched()
		ended := tick.Add(1)
		mu.Lock()
		spans = append(spans, span{began, ended})
		mu.Unlock()
		return nil
	})
	const publishers, rounds = 32, 20
	type publication struct{ renamed, answered int64 }
	done := make([][]publication, publishers)
	var wg sync.WaitGroup
	for p := range publishers {
		wg.Go(func() {
			for range rounds {
				renamed := tick.Add(1)
				if err := flushes.sync("docs"); err != nil {
					t.Errorf("sync: %v", err)
				}
				done[p] = append(done[p], publication{renamed, tick.Add(1)})
			}
		})
	}
	wg.Wait()
	for p, list := range done {
		for _, pub := range list {
			covered := false
			for _, s := range spans {
				if s.began > pub.renamed && s.ended < pub.answered {
					covered = true
					break
				}
			}
			if !covered {
				t.Fatalf("publisher %d renamed at %d and was answered at %d with no flush begun after its rename", p, pub.renamed, pub.answered)
			}
		}
	}
	if len(spans) >= publishers*rounds {
		t.Logf("%d flushes for %d publications: no two shared one on this run", len(spans), publishers*rounds)
	}
	if n := groupsInUse(flushes); n != 0 {
		t.Fatalf("%d directory groups held after every publication returned, want none", n)
	}
}

// TestASharedFlushFailureReachesEveryPublication pins the verdict: a failed
// directory flush is every participant's failure, never only the leader's —
// each publication then reports DIRECTORY_SYNC_FAILED for its own name.
func TestASharedFlushFailureReachesEveryPublication(t *testing.T) {
	t.Parallel()
	boom := errors.New("fsync: input/output error")
	flushes := newDirFlushes(func(string) error { return boom })
	verdicts := make([]error, 8)
	var wg sync.WaitGroup
	for i := range verdicts {
		wg.Go(func() { verdicts[i] = flushes.sync("spool") })
	}
	wg.Wait()
	for i, err := range verdicts {
		if !errors.Is(err, boom) {
			t.Errorf("publication %d: %v, want the flush's failure", i, err)
		}
	}
	if n := groupsInUse(flushes); n != 0 {
		t.Fatalf("%d directory groups held after every publication returned, want none", n)
	}
}

// TestDirectoriesFlushApart pins that sharing is per directory: a publication
// into one directory never takes another directory's flush as its own.
func TestDirectoriesFlushApart(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	flushed := map[string]int{}
	flushes := newDirFlushes(func(dir string) error {
		mu.Lock()
		flushed[dir]++
		mu.Unlock()
		return nil
	})
	var wg sync.WaitGroup
	for _, dir := range []string{"a", "b", "c", "a", "b", "c"} {
		wg.Go(func() {
			if err := flushes.sync(dir); err != nil {
				t.Errorf("sync(%s): %v", dir, err)
			}
		})
	}
	wg.Wait()
	for _, dir := range []string{"a", "b", "c"} {
		if flushed[dir] == 0 {
			t.Errorf("no flush of %s for the publications into it", dir)
		}
	}
}

// TestConcurrentPublicationsIntoOneDirectoryAllLand runs the shared flush on
// the disk engine: concurrent WriteAtomic calls into one directory each
// publish their own bytes, each answer nil, and no directory group outlives
// them.
func TestConcurrentPublicationsIntoOneDirectoryAllLand(t *testing.T) {
	t.Parallel()
	if !platformNative {
		t.Skip("NewOS refuses this platform")
	}
	built, err := NewOS(t.TempDir())
	if err != nil {
		t.Fatalf("NewOS: %v", err)
	}
	disk := built.(*osFS)
	t.Cleanup(func() { _ = disk.Close() })
	if err := disk.MkdirAll("ready", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const publications = 16
	var wg sync.WaitGroup
	for i := range publications {
		wg.Go(func() {
			name := "ready/m" + strconv.Itoa(i)
			if err := disk.WriteAtomic(name, []byte(name), 0o644); err != nil {
				t.Errorf("WriteAtomic(%s): %v", name, err)
			}
		})
	}
	wg.Wait()
	for i := range publications {
		name := "ready/m" + strconv.Itoa(i)
		got, err := fs.ReadFile(disk, name)
		if err != nil || string(got) != name {
			t.Errorf("ReadFile(%s) = %q, %v; want its own bytes", name, got, err)
		}
	}
	if n := groupsInUse(disk.flushes); n != 0 {
		t.Fatalf("%d directory groups held after every publication returned, want none", n)
	}
}
