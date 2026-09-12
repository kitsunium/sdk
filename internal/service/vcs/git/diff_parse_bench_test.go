// Package git — benchmarks for the changed-set parse/aggregation hot path,
// hosted in the dedicated `_bench_test.go` file per the symmetric
// KTN-TEST-SUFFIX contract (Benchmark* lives here, Test* lives in
// diff_parse_internal_test.go).
package git

import (
	"fmt"
	"strings"
	"testing"
)

// benchFileCount is the number of synthetic changed files rendered into the
// benchmark payloads — a deliberately busy branch (dozens of files) so the
// per-file parser state machine dominates over fixed overhead.
const benchFileCount int = 64

// benchHunksPerFile is the number of "+"-side hunks rendered per synthetic
// file, exercising the hunk-header parsing and line-range folding.
const benchHunksPerFile int = 4

// benchHunkStride spaces consecutive hunk start lines so ranges never overlap.
const benchHunkStride int = 20

// benchPackageFanout spreads the synthetic files across this many package
// directories so the touched-package aggregation sees realistic fan-out.
const benchPackageFanout int = 8

// syntheticDiffPayloads renders the two git wire payloads Resolve parses for a
// branch touching `files` Go files: the unified `git diff -U0 -M -C` output and
// the NUL-separated `git diff --name-status -z` membership list. Rendering them
// once up front stands in for the git subprocesses, keeping the benchmark
// hermetic (no git binary, no repository).
func syntheticDiffPayloads(files, hunksPerFile int) (unified, nameStatus string) {
	var diff strings.Builder
	var ns strings.Builder
	//: One "diff --git" block + one name-status record per synthetic file.
	for i := range files {
		rel := fmt.Sprintf("pkg/p%02d/file%03d.go", i%benchPackageFanout, i)
		fmt.Fprintf(&diff, "diff --git a/%s b/%s\n", rel, rel)
		fmt.Fprintf(&diff, "index 0000000..1111111 100644\n")
		fmt.Fprintf(&diff, "--- a/%s\n", rel)
		fmt.Fprintf(&diff, "+++ b/%s\n", rel)
		//: Each hunk adds three lines at a strided, non-overlapping offset.
		for h := range hunksPerFile {
			start := 10 + h*benchHunkStride
			fmt.Fprintf(&diff, "@@ -%d,2 +%d,3 @@ func F%d() {\n", start, start, h)
			diff.WriteString("+added line one\n+added line two\n+added line three\n")
		}
		ns.WriteString("M\x00")
		ns.WriteString(rel)
		ns.WriteString("\x00")
	}
	//: Return both pre-rendered payloads to the benchmark.
	return diff.String(), ns.String()
}

// BenchmarkGitResolve pins the parse/aggregation path of Resolve: folding the
// name-status membership list and the unified -U0 diff payload into a fresh
// ChangedSetValue, in the same order addDiff applies them. The git subprocess
// layer is stubbed out by the pre-rendered payloads (real git execution is
// covered by the temp-repo tests in resolve_external_test.go), so the loop
// measures pure parsing + changed-set aggregation. The nil IncludeFunc mirrors
// the hermetic internal-test wiring and keeps the filesystem out of the loop.
func BenchmarkGitResolve(b *testing.B) {
	unified, nameStatus := syntheticDiffPayloads(benchFileCount, benchHunksPerFile)
	root := "/repo"
	b.ReportAllocs()
	for b.Loop() {
		set := NewChangedSetValue(root)
		//: Membership pass first, then line ranges — mirrors addDiff's order.
		parseNameStatus(nameStatus, root, set, nil)
		parseUnifiedDiff(unified, root, set, nil)
		//: A populated payload must yield a populated set; guard against a
		//: parser regression silently turning the benchmark into a no-op.
		if set.IsEmpty() {
			b.Fatal("synthetic payload produced an empty changed-set")
		}
	}
}
