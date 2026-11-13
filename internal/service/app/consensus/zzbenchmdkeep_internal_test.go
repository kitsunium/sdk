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
	type tc struct {
		name string
		// rendered is the generator's output, before the file it replaces.
		rendered string
		before   []byte
		// ok judges the regenerated file.
		ok func(got string) bool
	}
	cases := []tc{
		{
			name:     "a hand-run part before",
			rendered: "<!-- generated -->\n# Benchmarks\n\n| new | row |\n",
			before:   []byte("<!-- generated -->\n# Benchmarks\n\n| old | row |\n\n" + hand),
			ok: func(got string) bool {
				return strings.HasSuffix(got, "\n\n"+hand) && strings.Contains(got, "| new | row |") && !strings.Contains(got, "| old | row |")
			},
		},
		{
			name:     "no file before",
			rendered: "generated\n",
			before:   nil,
			ok:       func(got string) bool { return got == "generated\n" },
		},
		{
			name:     "no hand-run part before",
			rendered: "generated\n",
			before:   []byte("no hand-run part\n"),
			ok:       func(got string) bool { return got == "generated\n" },
		},
	}
	runCase := func(t *testing.T, c tc) {
		if got := keepHandRun(c.rendered, c.before); !c.ok(got) {
			t.Fatalf("the regenerated file is\n%s", got)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
