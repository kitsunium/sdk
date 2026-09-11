package vfs_test

import (
	"bytes"
	"io/fs"
	"runtime"
	"sync"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// tornPayloadBytes is deliberately far larger than a page, so that the window
// between "the file has been truncated" and "the file has been rewritten" is
// wide enough for a concurrent reader to fall into. At 64 KiB the in-place
// writer needs several trips through the kernel, and the reader needs one.
const tornPayloadBytes int = 64 << 10

// tornRounds is how many times each half of the comparison publishes. It is the
// same for both so the two counts below are comparable.
const tornRounds int = 300

// tornVersions builds the two complete contents a reader is ever allowed to
// see. They differ in every byte, so a mixture of the two is as detectable as a
// truncation is.
func tornVersions() (first, second []byte) {
	//: distinct fill bytes make a spliced read — the first half of one version
	//: followed by the second half of the other — fail the comparison too,
	//: which a length check on its own would miss.
	return bytes.Repeat([]byte{'a'}, tornPayloadBytes), bytes.Repeat([]byte{'b'}, tornPayloadBytes)
}

// countTornReads races one writer against one reader over the same name and
// reports how many reads observed something that is neither complete version.
//
// publish is the verb under test, which is the only thing that differs between
// the two halves of TestOnlyTheAtomicWriterSurvivesAConcurrentReader.
func countTornReads(t *testing.T, filesystem corevfs.FullFS, name string,
	publish func(payload []byte) error,
) (torn int) {
	t.Helper()
	first, second := tornVersions()
	if seedErr := publish(first); seedErr != nil {
		t.Fatalf("seeding %q: %v", name, seedErr)
	}

	var writer sync.WaitGroup
	writer.Go(func() {
		for round := range tornRounds {
			payload := first
			//: alternate, so every publication genuinely replaces the content
			//: rather than rewriting the same bytes over themselves.
			if round%2 == 1 {
				payload = second
			}
			if publishErr := publish(payload); publishErr != nil {
				t.Errorf("publishing round %d: %v", round, publishErr)
				return
			}
		}
	})

	//: the reader runs on this goroutine and keeps going until the writer is
	//: finished, so the race window is sampled for the whole publication run.
	for range tornRounds * 2 {
		got, readErr := fs.ReadFile(filesystem, name)
		//: an absent name is itself a torn observation: the point of the
		//: comparison is that SOME readers lose, not that they crash.
		if readErr != nil {
			torn++
			continue
		}
		//: neither complete version means the reader saw a file mid-flight.
		if !bytes.Equal(got, first) && !bytes.Equal(got, second) {
			torn++
		}
	}
	writer.Wait()
	//: the evidence.
	return torn
}

// TestOnlyTheAtomicWriterSurvivesAConcurrentReader is the measurement behind
// the claim the whole domain rests on.
//
// Every other test in this package proves what WriteAtomic does. This one
// proves that the alternative is genuinely worse, under an IDENTICAL harness —
// same payload, same round count, same reader loop, same filesystem — so the
// only variable is the verb. Without it, "an in-place write leaves a reader
// holding a truncated file" is a claim the SDK asserts about itself and never
// checks.
//
// The assertion is deliberately ONE-SIDED, and that is not timidity. Zero torn
// reads from WriteAtomic is an invariant: rename(2) is atomic, so there is no
// scheduling in which a reader can observe a partial file, and any count above
// zero is a real defect. A non-zero count from WriteFile, by contrast, is a
// RACE being won — it depends on the scheduler, the filesystem and the machine,
// so requiring it would make this test fail on a fast enough disk for a reason
// that is not a bug. The WriteFile count is therefore measured and logged
// rather than asserted, and BENCH.md records what it actually was.
func TestOnlyTheAtomicWriterSurvivesAConcurrentReader(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("%s has no NewOS to test", runtime.GOOS)
	}
	filesystem, newErr := svcvfs.NewOS(t.TempDir())
	if newErr != nil {
		t.Fatalf("NewOS = %v, want a filesystem", newErr)
	}

	inPlace := countTornReads(t, filesystem, "in-place.json", func(payload []byte) error {
		//: the ordinary verb: truncate in place, then write.
		return filesystem.WriteFile("in-place.json", payload, 0o644)
	})
	published := countTornReads(t, filesystem, "published.json", func(payload []byte) error {
		//: temporary beside the target, flush, rename, flush the directory.
		return filesystem.WriteAtomic("published.json", payload, 0o644)
	})

	//: THE invariant. rename(2) leaves no scheduling in which a reader sees a
	//: partial file, so any count at all here is a defect and not a flake.
	if published != 0 {
		t.Errorf("WriteAtomic exposed %d torn reads out of %d — publication was not atomic",
			published, tornRounds*2)
	}
	//: the contrast, measured rather than assumed. A zero here means the race
	//: window was not hit on this machine, NOT that WriteFile is safe — so it
	//: is reported and never asserted.
	t.Logf("torn reads out of %d: WriteFile=%d, WriteAtomic=%d", tornRounds*2, inPlace, published)
	if inPlace == 0 {
		t.Log("WriteFile exposed no torn read on this run — the window was not hit; " +
			"this is a property of the scheduler, not evidence that writing in place is safe")
	}
}
