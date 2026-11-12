package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// isTempHex reports whether s is n lower-case hex digits.
func isTempHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// TestTheOrdinaryTemporaryNameIsUniqueAndBesideItsTarget pins the name built
// in one allocation (#813): dir/.vfs-<32 hex digits>.tmp — the seed the
// process drew once, then its counter —, a bare base name at the root, and
// no two names alike however many publications draw one at once.
func TestTheOrdinaryTemporaryNameIsUniqueAndBesideItsTarget(t *testing.T) {
	t.Parallel()
	seed, seedErr := tempSeed()
	if seedErr != nil || !isTempHex(seed, 2*tempSeedBytes) {
		t.Fatalf("tempSeed() = %q, %v, want %d hex digits", seed, seedErr, 2*tempSeedBytes)
	}
	const drawers, draws = 8, 500
	names := make([][]string, drawers)
	var wg sync.WaitGroup
	for d := range drawers {
		wg.Go(func() {
			for range draws {
				name, err := tempPath("queue/ready")
				if err != nil {
					t.Errorf("tempPath = %v", err)
					return
				}
				names[d] = append(names[d], name)
			}
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, list := range names {
		for _, name := range list {
			if seen[name] {
				t.Fatalf("tempPath drew %q twice", name)
			}
			seen[name] = true
			dir, base := path.Split(name)
			hexPart, ok := strings.CutPrefix(base, tempPrefix)
			hexPart, ok2 := strings.CutSuffix(hexPart, tempSuffix)
			if dir != "queue/ready/" || !ok || !ok2 || !isTempHex(hexPart, 32) || !strings.HasPrefix(hexPart, seed) {
				t.Fatalf("tempPath = %q, want queue/ready/%s<the seed %s><16 hex digits>%s", name, tempPrefix, seed, tempSuffix)
			}
		}
	}
	if root, err := tempPath("."); err != nil || strings.Contains(root, "/") || !strings.HasPrefix(root, tempPrefix) {
		t.Fatalf("tempPath(\".\") = %q, %v, want a bare base name", root, err)
	}
	long := strings.Repeat("d/", 100) + "e"
	if name, err := tempPath(long); err != nil || path.Dir(name) != long {
		t.Fatalf("tempPath(a %d-byte directory) = %q, %v, want a name in it", len(long), name, err)
	}
}

// takenOps is the production mechanics with create refusing the first taken
// names as already existing, and recording every name it was asked for.
func takenOps(disk *osFS, taken int, names *[]string) atomicOps {
	ops := disk.nativeOps()
	native := ops.create
	ops.create = func(name string, perm fs.FileMode) (tempFile, error) {
		*names = append(*names, name)
		if len(*names) <= taken {
			//: what OpenFile with O_EXCL reports for a planted name.
			return nil, &fs.PathError{Op: "openat", Path: name, Err: fs.ErrExist}
		}
		return native(name, perm)
	}
	return ops
}

// TestATakenTemporaryNameFallsBackToARandomOne pins the bounded retry (#813):
// a name already taken is never opened, written or removed — O_EXCL refused
// it —, the publication tries a random name instead, beside the target, and
// lands; once tempAttempts names are taken it is PUBLISH_FAILED with the
// cause kept, the destination untouched and nothing left behind; and a
// refusal other than "taken" is not retried.
func TestATakenTemporaryNameFallsBackToARandomOne(t *testing.T) {
	t.Parallel()
	for taken := 1; taken < tempAttempts; taken++ {
		t.Run(strconv.Itoa(taken)+" taken", func(t *testing.T) {
			t.Parallel()
			disk, dir := newDiskFS(t)
			if err := disk.MkdirAll("ready", 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			var names []string
			disk.ops = takenOps(disk, taken, &names)
			if err := disk.WriteAtomic("ready/m", []byte("landed"), 0o644); err != nil {
				t.Fatalf("WriteAtomic with %d name(s) taken = %v, want nil", taken, err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "ready", "m")); err != nil || string(got) != "landed" {
				t.Fatalf("ready/m = %q, %v, want the publication", got, err)
			}
			if len(names) != taken+1 {
				t.Fatalf("%d names tried, want %d", len(names), taken+1)
			}
			for i, name := range names {
				if path.Dir(name) != "ready" {
					t.Fatalf("name %d = %q, not beside its target", i, name)
				}
				for _, earlier := range names[:i] {
					if earlier == name {
						t.Fatalf("name %q tried twice", name)
					}
				}
			}
			if orphans := leftoverTemps(t, filepath.Join(dir, "ready")); len(orphans) != 0 {
				t.Fatalf("temporaries left behind: %v", orphans)
			}
		})
	}
	t.Run("every name taken", func(t *testing.T) {
		t.Parallel()
		disk, dir := newDiskFS(t)
		if err := os.WriteFile(filepath.Join(dir, "m"), []byte("previous"), 0o600); err != nil {
			t.Fatal(err)
		}
		var names []string
		disk.ops = takenOps(disk, tempAttempts, &names)
		err := disk.WriteAtomic("m", []byte("never"), 0o644)
		if !kerrs.HasCode(err, corevfs.CodePublishFailed) || !errors.Is(err, fs.ErrExist) {
			t.Fatalf("WriteAtomic with every name taken = %v, want PUBLISH_FAILED over ErrExist", err)
		}
		if len(names) != tempAttempts {
			t.Fatalf("%d names tried, want the bound, %d", len(names), tempAttempts)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "m")); string(got) != "previous" {
			t.Fatalf("m = %q after a refused publication, want the previous bytes", got)
		}
	})
	t.Run("another refusal", func(t *testing.T) {
		t.Parallel()
		disk, _ := newDiskFS(t)
		var tries int
		ops := disk.nativeOps()
		ops.create = func(name string, _ fs.FileMode) (tempFile, error) {
			tries++
			return nil, &fs.PathError{Op: "openat", Path: name, Err: fs.ErrPermission}
		}
		disk.ops = ops
		if err := disk.WriteAtomic("m", []byte("never"), 0o644); !kerrs.HasCode(err, corevfs.CodePublishFailed) || tries != 1 {
			t.Fatalf("WriteAtomic over a permission refusal = %v after %d tries, want PUBLISH_FAILED after one", err, tries)
		}
	})
}

// TestAPlantedNextNameIsNeitherFollowedNorOverwritten plants, on the real
// disk engine, the very name the next publication will try first — the
// counter is the process's, so a planter that read one name can guess the
// next — as a link to a file of the tree: O_EXCL refuses it, the publication
// lands through a random name, and neither the link nor its target moves.
//
// Not parallel: the planted name is the counter's next value, which another
// test's publication would take first.
func TestAPlantedNextNameIsNeitherFollowedNorOverwritten(t *testing.T) {
	disk, dir := newDiskFS(t)
	if err := os.WriteFile(filepath.Join(dir, "victim"), []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, seedErr := tempSeed()
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	next := string(appendTempCounter([]byte(tempPrefix+seed), tempCounter.Load()+1)) + tempSuffix
	if err := os.Symlink("victim", filepath.Join(dir, next)); err != nil {
		t.Skipf("no symbolic link here: %v", err)
	}
	if err := disk.WriteAtomic("m", []byte("landed"), 0o644); err != nil {
		t.Fatalf("WriteAtomic past a planted name = %v, want nil", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "m")); err != nil || string(got) != "landed" {
		t.Fatalf("m = %q, %v, want the publication", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "victim")); string(got) != "untouched" {
		t.Fatalf("the planted link's target = %q, want it untouched", got)
	}
	if target, err := os.Readlink(filepath.Join(dir, next)); err != nil || target != "victim" {
		t.Fatalf("the planted link = %q, %v, want it left as it was", target, err)
	}
}

// TestADestinationNamedLikeTheNextTemporaryIsNeverItsOwnTemporary publishes
// to the very name the next ordinary temporary would take — a valid name, and
// one a caller can read off a listing during another publication: the
// temporary is drawn again, so the destination is never created and written
// in place as its own temporary, half-visible to a reader; it is published
// by a rename like any other, and nothing else is left in the directory.
//
// Not parallel: the destination is the counter's next value, which another
// test's publication would take first.
func TestADestinationNamedLikeTheNextTemporaryIsNeverItsOwnTemporary(t *testing.T) {
	disk, dir := newDiskFS(t)
	seed, seedErr := tempSeed()
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	target := string(appendTempCounter([]byte(tempPrefix+seed), tempCounter.Load()+1)) + tempSuffix
	var names []string
	disk.ops = takenOps(disk, 0, &names)
	if err := disk.WriteAtomic(target, []byte("landed"), 0o644); err != nil {
		t.Fatalf("WriteAtomic(%s) = %v, want nil", target, err)
	}
	for _, name := range names {
		if name == target {
			t.Fatalf("the destination %s was created as its own temporary: its bytes were visible before the publication", target)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, target)); err != nil || string(got) != "landed" {
		t.Fatalf("%s = %q, %v, want the publication", target, got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != target {
		t.Fatalf("the directory holds %v, %v, want the destination alone", entries, err)
	}
}
