package vfs_test

import (
	"io/fs"
	"sync"
	"testing"
	"time"

	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// TestTheMemoryFilesystemReportsNoModificationTime pins a documented ABSENCE.
//
// A filesystem with no device has no clock either, and inventing one would
// mean reaching for the wall clock in a type whose whole purpose is to make
// tests deterministic. The zero time is the honest answer, and this test
// exists so nobody quietly replaces it with time.Now() — which would work
// today and produce a flaky consumer test six months from now.
func TestTheMemoryFilesystemReportsNoModificationTime(t *testing.T) {
	t.Parallel()
	filesystem := svcvfs.NewMem()
	if err := filesystem.WriteFile("note.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile = %v, want nil", err)
	}
	info, statErr := fs.Stat(filesystem, "note.txt")
	if statErr != nil {
		t.Fatalf("fs.Stat = %v, want nil", statErr)
	}
	if !info.ModTime().Equal(time.Time{}) {
		t.Fatalf("ModTime = %v, want the zero time — see memInfo's doc comment", info.ModTime())
	}
}

// TestTheMemoryFilesystemHandsBackCopiesNotViews pins the property that makes
// it safe under -race and safe against a caller that keeps its slice.
//
// A double that returned the stored slice would let a test mutate the
// filesystem by appending to something it read out of it, and the bug would
// surface as an unrelated assertion failing three tests later.
func TestTheMemoryFilesystemHandsBackCopiesNotViews(t *testing.T) {
	t.Parallel()
	filesystem := svcvfs.NewMem()
	original := []byte("stored")
	if err := filesystem.WriteFile("note.txt", original, 0o644); err != nil {
		t.Fatalf("WriteFile = %v, want nil", err)
	}
	//: mutating the slice the CALLER still holds must not reach the store.
	original[0] = 'X'
	first, readErr := fs.ReadFile(filesystem, "note.txt")
	if readErr != nil {
		t.Fatalf("fs.ReadFile = %v, want nil", readErr)
	}
	if string(first) != "stored" {
		t.Fatalf("content = %q, want %q — the write kept the caller's slice", first, "stored")
	}
	//: and mutating the slice the caller was HANDED must not reach it either.
	first[0] = 'Y'
	second, readErr := fs.ReadFile(filesystem, "note.txt")
	if readErr != nil {
		t.Fatalf("fs.ReadFile = %v, want nil", readErr)
	}
	if string(second) != "stored" {
		t.Fatalf("content = %q, want %q — the read handed back a view", second, "stored")
	}
}

// TestTheMemoryFilesystemPublishesAtomicallyUnderConcurrentReaders is the
// memory counterpart of the disk test with the same name. There is no
// temporary file here and no device: the guarantee comes from the single map
// assignment happening under the exclusive lock, and this is what proves it
// holds under -race rather than by inspection.
func TestTheMemoryFilesystemPublishesAtomicallyUnderConcurrentReaders(t *testing.T) {
	t.Parallel()
	filesystem := svcvfs.NewMem()
	const name = "feed.json"
	first := []byte(`{"v":1}`)
	second := []byte(`{"v":2,"and":"a good deal longer than the first"}`)
	if err := filesystem.WriteAtomic(name, first, 0o644); err != nil {
		t.Fatalf("seeding = %v, want nil", err)
	}

	var writers sync.WaitGroup
	writers.Go(func() {
		for i := range 500 {
			payload := first
			if i%2 == 1 {
				payload = second
			}
			if err := filesystem.WriteAtomic(name, payload, 0o644); err != nil {
				t.Errorf("WriteAtomic = %v, want nil", err)
				return
			}
		}
	})
	for range 500 {
		got, readErr := fs.ReadFile(filesystem, name)
		if readErr != nil {
			t.Fatalf("fs.ReadFile = %v, want nil — a published name must always resolve", readErr)
		}
		if string(got) != string(first) && string(got) != string(second) {
			t.Fatalf("a reader saw %q, which is neither version", got)
		}
	}
	writers.Wait()
}
