package consensus

import (
	"errors"
	"io/fs"
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

// benchBefore is the BENCH.md at path that the generator replaces, read
// with read: nil when there is none yet, nothing hand-run to keep, and an
// error when it exists but cannot be read. Carried on as empty, it would
// have the write erase every hand-run section.
func benchBefore(read func(string) ([]byte, error), path string) ([]byte, error) {
	b, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
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

// The generator read the BENCH.md it replaces ignoring the error: a file
// it could not read was taken as empty and the write erased its hand-run
// sections. A missing file is the only one taken as empty.
func TestTheBenchGeneratorWritesNothingOverAReportItCannotRead(t *testing.T) {
	type tc struct {
		name string
		// read answers the generator's read of the report.
		read    func(string) ([]byte, error)
		want    string
		wantErr bool
	}
	cases := []tc{
		{name: "read", read: func(string) ([]byte, error) { return []byte("report"), nil }, want: "report"},
		{name: "none yet", read: func(p string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}},
		{name: "unreadable", read: func(p string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
		}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		got, err := benchBefore(c.read, "BENCH.md")
		if (err != nil) != c.wantErr || string(got) != c.want {
			t.Fatalf("benchBefore = %q, %v; want %q, an error %t", got, err, c.want, c.wantErr)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
