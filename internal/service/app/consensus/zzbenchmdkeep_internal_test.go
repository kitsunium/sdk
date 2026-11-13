package consensus

import (
	"strings"
	"testing"
)

// benchHandRunHeading opens the part of BENCH.md no generator writes: the
// hand-run sections of #913, #922, #923 and #1109, each refreshed by the
// command it names.
const benchHandRunHeading = "## Hand-run micro-benchmarks"

// keepHandRun is rendered followed by the hand-run part of before, the
// file it replaces, from benchHandRunHeading on: the generator rewrote the
// whole file and erased them (#1361's known follow-up). It sits in a file
// of its own, in consensus_test and in the bench lane's target alike.
func keepHandRun(rendered string, before []byte) string {
	i := strings.Index(string(before), "\n"+benchHandRunHeading)
	if i < 0 {
		return rendered
	}
	return strings.TrimRight(rendered, "\n") + "\n" + string(before[i:])
}

// make bench rewrote all of BENCH.md and erased the hand-run sections of
// #913, #922, #923 and #1109 below the generator's output, which only
// their own commands refresh (#1361's known follow-up): the generator
// keeps everything from benchHandRunHeading on.
func TestTheBenchGeneratorKeepsTheHandRunSections(t *testing.T) {
	const hand = "## Hand-run micro-benchmarks (#913)\n\nmeasured by hand\n"
	old := "<!-- generated -->\n# Benchmarks\n\n| old | row |\n\n" + hand
	got := keepHandRun("<!-- generated -->\n# Benchmarks\n\n| new | row |\n", []byte(old))
	if !strings.HasSuffix(got, "\n\n"+hand) || !strings.Contains(got, "| new | row |") || strings.Contains(got, "| old | row |") {
		t.Fatalf("the regenerated file is\n%s", got)
	}
	if got := keepHandRun("generated\n", nil); got != "generated\n" {
		t.Fatalf("with no file before: %q", got)
	}
	if got := keepHandRun("generated\n", []byte("no hand-run part\n")); got != "generated\n" {
		t.Fatalf("with no hand-run part before: %q", got)
	}
}
