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

// benchSplice is doc with section in place of the section that opens with
// the line heading, up to the next second-level heading: a run of the
// generator that measures some sections alone (CONSENSUS_BENCH_ONLY, #1364)
// rewrites them and keeps every other byte. A doc without that section gets
// it before the first of anchors it holds, at its end otherwise.
func benchSplice(doc, heading, section string, anchors ...string) string {
	if i := benchHeadingAt(doc, heading); i >= 0 {
		end := len(doc)
		if j := strings.Index(doc[i+len(heading):], "\n## "); j >= 0 {
			end = i + len(heading) + j
		}
		return doc[:i] + section + doc[end:]
	}
	for _, anchor := range anchors {
		if i := benchHeadingAt(doc, anchor); i >= 0 {
			return doc[:i] + section + "\n" + doc[i:]
		}
	}
	return strings.TrimRight(doc, "\n") + "\n\n" + section
}

// benchHeadingAt is the offset of the first line of doc that opens with
// heading, -1 when none does.
func benchHeadingAt(doc, heading string) int {
	if strings.HasPrefix(doc, heading) {
		return 0
	}
	if i := strings.Index(doc, "\n"+heading); i >= 0 {
		return i + 1
	}
	return -1
}

// A run that measures some sections alone rewrites each in place and keeps
// every other byte of BENCH.md, the rows of the devices it did not measure
// and the hand-run part included; a section the file does not hold yet goes
// before the first anchor it holds (#1364).
func TestTheBenchGeneratorRewritesASectionInPlace(t *testing.T) {
	const (
		head    = "<!-- generated -->\n# Benchmarks\n\n## Host\n\n| CPU | x |\n"
		turn    = "## Turn\n\nold turn rows\n"
		targets = "## Targets\n\n| met |\n"
		hand    = "## Hand-run micro-benchmarks (#913)\n\nmeasured by hand\n"
		fresh   = "## Turn\n\nnew turn rows\n"
	)
	type tc struct {
		name    string
		doc     string
		anchors []string
		want    string
	}
	cases := []tc{
		{
			name: "the section in place, between two others",
			doc:  head + "\n" + turn + "\n" + targets + "\n" + hand,
			want: head + "\n" + fresh + "\n" + targets + "\n" + hand,
		},
		{
			name: "the section last of the file",
			doc:  head + "\n" + turn,
			want: head + "\n" + fresh,
		},
		{
			name:    "no such section: before the first anchor the file holds",
			doc:     head + "\n" + targets + "\n" + hand,
			anchors: []string{"## Reproduce", "## Targets"},
			want:    head + "\n" + fresh + "\n" + targets + "\n" + hand,
		},
		{
			name: "no such section, no anchor: at the end",
			doc:  head,
			want: head + "\n" + fresh,
		},
		{
			name: "a heading in a line's middle is no section",
			doc:  head + "see ## Turn below\n",
			want: head + "see ## Turn below\n\n" + fresh,
		},
	}
	runCase := func(t *testing.T, c tc) {
		if got := benchSplice(c.doc, "## Turn", fresh, c.anchors...); got != c.want {
			t.Fatalf("the rewritten file is\n%s\nwant\n%s", got, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
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
