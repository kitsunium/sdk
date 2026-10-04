// Package main — the platforms table: the GOOS/GOARCH cells every judgement
// genindex makes is made on, read from scripts/ci/platforms.sh.
package main

import (
	"fmt"
	"go/build"
	"os"
	"regexp"
	"strings"
)

const (
	// tableOpen is the line that opens the table in scripts/ci/platforms.sh:
	// the here-document the script prints and genindex reads without running
	// the script.
	tableOpen string = "cat <<'CELLS'"
	// tableClose is the line that closes it.
	tableClose string = "CELLS"
	// defaultPlatforms is where the table sits relative to tools/genindex,
	// the directory every make recipe runs this program from.
	defaultPlatforms string = "../../scripts/ci/platforms.sh"
)

// cellPattern is one cell of the table: goos/goarch, the spelling the
// cross-build matrix and the go command use.
var cellPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// platform is one GOOS/GOARCH pair a doc comment is judged under and an API
// is read on.
type platform struct {
	// goos is the target operating system.
	goos string
	// goarch is the target architecture.
	goarch string
}

// String renders a platform the way GOOS/GOARCH is written.
func (p platform) String() string {
	//: the conventional spelling.
	return p.goos + "/" + p.goarch
}

// matches reports whether the go command compiles the named file of dir for
// this platform: its name suffix and its build constraints, with cgo off, as
// every lane of this repository builds.
func (p platform) matches(dir, name string) (bool, error) {
	ctx := build.Default
	ctx.GOOS = p.goos
	ctx.GOARCH = p.goarch
	ctx.CgoEnabled = false
	//: the go command's own answer, not a re-implementation of it.
	return ctx.MatchFile(dir, name)
}

// readPlatforms reads the cells of the table at path, in its order.
//
// The script is read, never run: its here-document is the table, so genindex
// needs no shell, and a Bazel test reads it as a plain data file.
func readPlatforms(path string) (cells []platform, err error) {
	src, rerr := os.ReadFile(path)
	//: without the table there is no cell to judge anything on.
	if rerr != nil {
		//: name the table that could not be read.
		return nil, fmt.Errorf("platforms table: %w", rerr)
	}
	parsed, perr := parsePlatforms(string(src))
	//: a table in another shape would judge on cells nobody listed.
	if perr != nil {
		//: name the table and what is wrong with it.
		return nil, fmt.Errorf("platforms table %s: %w", path, perr)
	}
	//: the cells, in the table's order.
	return parsed, nil
}

// parsePlatforms returns the cells written between the line that opens the
// here-document and the line that closes it.
//
// It refuses rather than guesses: no table, a table left open, an empty one,
// a line that is no goos/goarch pair, or a cell written twice. A carriage
// return a Windows checkout adds is ignored, as the shell would not.
func parsePlatforms(src string) (cells []platform, err error) {
	var out []platform
	seen := map[string]struct{}{}
	open, closed := false, false
	//: line by line, the newline that ends the file ending no line: the
	//: table is one here-document.
	for line := range strings.SplitSeq(strings.TrimSuffix(src, "\n"), "\n") {
		line = strings.TrimSpace(line)
		//: the lines before the here-document are the script's own.
		if !open {
			open = line == tableOpen
			continue
		}
		//: the delimiter ends the table.
		if line == tableClose {
			closed = true
			break
		}
		cell, cerr := parseCell(line, seen)
		//: a line that is no cell, or one already listed.
		if cerr != nil {
			//: say which.
			return nil, cerr
		}
		out = append(out, cell)
	}
	//: the table must exist, end and hold at least one cell.
	if verr := tableState(open, closed, len(out)); verr != nil {
		//: what is missing.
		return nil, verr
	}
	//: every cell, in the order written.
	return out, nil
}

// parseCell reads one line of the table as a cell, refusing a malformed line
// and a cell already seen.
func parseCell(line string, seen map[string]struct{}) (platform, error) {
	//: the go command's spelling, nothing else.
	if !cellPattern.MatchString(line) {
		//: quote the line so a stray space is visible.
		return platform{}, fmt.Errorf("%q is no goos/goarch cell", line)
	}
	//: one table holds a cell once.
	if _, dup := seen[line]; dup {
		//: a duplicate would be judged twice and reported twice.
		return platform{}, fmt.Errorf("cell %s is listed twice", line)
	}
	seen[line] = struct{}{}
	goos, goarch, _ := strings.Cut(line, "/")
	//: the parsed cell.
	return platform{goos: goos, goarch: goarch}, nil
}

// tableState refuses a table that was never opened, never closed or empty.
func tableState(open, closed bool, n int) error {
	//: no here-document at all.
	if !open {
		//: name the line the table must start with.
		return fmt.Errorf("no line %q opens the table", tableOpen)
	}
	//: a here-document left open would be the rest of the file.
	if !closed {
		//: name the delimiter that is missing.
		return fmt.Errorf("the table is never closed by a line %q", tableClose)
	}
	//: an empty table judges nothing, which must not read as passing.
	if n == 0 {
		//: refuse it.
		return fmt.Errorf("the table lists no cell")
	}
	//: a usable table.
	return nil
}

// cellNames renders cells as their goos/goarch spellings, in order.
func cellNames(cells []platform) []string {
	out := make([]string, 0, len(cells))
	//: one spelling per cell.
	for _, c := range cells {
		out = append(out, c.String())
	}
	//: the spellings.
	return out
}
