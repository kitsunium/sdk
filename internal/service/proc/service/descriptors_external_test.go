package service_test

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The descriptor table the leak tests compare (TestInstancesLeakNothing,
// TestRealFDStoreLeaksNothing), on Linux; elsewhere /proc says nothing and
// they compare nothing. A window is measured from a settled table and closed
// without a collection: what it opened and left is its leak, even one the
// collector would close later, and nothing it did not open counts.

// settleRounds bounds the collections settledDescriptors runs before it
// gives up on a table that never holds still.
const settleRounds = 200

// descriptors lists this process's open descriptors, each as its number and
// what /proc/self/fd says it refers to, sorted; nil off Linux.
func descriptors(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		target, linkErr := os.Readlink("/proc/self/fd/" + e.Name())
		if linkErr != nil {
			continue // the directory handle ReadDir held, closed since
		}
		out = append(out, e.Name()+" -> "+target)
	}
	slices.Sort(out)
	return out
}

// settledDescriptors is the table a window starts from: this process's
// descriptors once nothing but the test changes them. Two changes otherwise
// fall inside the window and read as the test's:
//   - the runtime's poller — epoll and its eventfd — is opened lazily, at the
//     first pollable descriptor: the test's own first pipe when it runs
//     alone. A pipe opened and closed here opens it first.
//   - a descriptor an earlier test left to the collector — an adopted
//     process's pidfd, which proc/exec leaves to the runtime once the handle
//     is collected (TestRealForking*, a Type=forking unit) — is closed by a
//     collection the window's allocations trigger. Collections run here until
//     three tables in a row, a few milliseconds apart, are the same.
func settledDescriptors(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = w.Close()
	var last []string
	still := 0
	for range settleRounds {
		runtime.GC()
		time.Sleep(2 * time.Millisecond) // finalizers and cleanups run on their own goroutines
		now := descriptors(t)
		if slices.Equal(now, last) {
			still++
		} else {
			still = 0
		}
		if still == 3 {
			return now
		}
		last = now
	}
	t.Fatalf("the descriptor table never held still over %d collections: %s", settleRounds, strings.Join(last, ", "))
	return nil
}

// sameDescriptors fails t when after is not before: a descriptor the window
// opened and left, or one it closed that was not its own.
func sameDescriptors(t *testing.T, window string, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("descriptors before %s:\n%s\nafter:\n%s", window, strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}
