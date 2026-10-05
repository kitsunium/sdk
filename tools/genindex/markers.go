// Package main — the pin markers: on each cell, the code's exported
// (id, kind, canonical signature) set of a package equals the set the
// `// go:<id> <kind> <canonical signature>` markers of its api_gen*_test.go
// pin files declare. kit writes the pins and the markers from the design;
// genindex reads them with go/parser and never reads the design.
//
// A design that owns its docs (ADR 0167) puts ` doc:<sha256>` after each
// marker's signature and `// package <path> doc:<sha256>` in each pin file:
// the digest of the symbol's doc as docs/api records it — a struct's
// exported fields' docs after its own, each as NUL, its name, NUL, its doc —
// and of the package comment. On each cell the code's docs hash to them, or
// the doc differs: a doc edited without the design.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// markerPrefix opens a marker line: a comment, a space, then the id —
	// unlike a //go: directive, which has no space.
	markerPrefix string = "// go:"
	// markerDocPrefix opens the doc digest a marker ends with, after its
	// signature, when the design owns its docs.
	markerDocPrefix string = " doc:"
	// packageMarkerPrefix opens a pin file's package marker: the package
	// comment's digest.
	packageMarkerPrefix string = "// package "
	// digestLen is a sha256's length in hex.
	digestLen int = 64
	// pinGlob matches a pin file's name.
	pinGlob string = "api_gen*_test.go"
)

var (
	// apiKinds are the kinds docs/api writes, which a marker names.
	apiKinds = []string{kindFunc, kindMethod, kindType, kindAlias, kindConst, kindVar}

	// quotedRun is a Go string literal as go/types prints a struct tag: what
	// a tag-only difference between two canonical signatures consists of.
	quotedRun = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
)

// marker is one pin marker: what kit says the design declares.
type marker struct {
	// id is the symbol's go: id.
	id string
	// kind is its kind.
	kind string
	// canonical is its canonical signature.
	canonical string
	// doc is its doc digest, "" when the design does not own its docs.
	doc string
	// file is the pin file, by name.
	file string
	// line is the marker's line.
	line int
}

// pinFile is one api_gen*_test.go file of a package directory.
type pinFile struct {
	// name is its base name.
	name string
	// markers are its well-formed markers.
	markers []marker
	// pkgDoc is its package marker's digest, "" when it has none.
	pkgDoc string
}

// markerSet is one directory's pin files and the findings parsing them gave.
type markerSet struct {
	// pins are the pin files, by name.
	pins []pinFile
	// problems are the malformed markers and the unreadable files.
	problems []string
}

// readPins parses the pin files of a directory: every marker line of each.
func readPins(dir string) markerSet {
	var set markerSet
	names, err := filepath.Glob(filepath.Join(dir, pinGlob))
	//: the pattern is a constant the glob syntax accepts; a failure says so.
	if err != nil {
		set.problems = append(set.problems, "the pin files cannot be listed: "+err.Error())
	}
	slices.Sort(names)
	//: each pin file, by name.
	for _, path := range names {
		pin, problems := readPin(path)
		set.pins = append(set.pins, pin)
		set.problems = append(set.problems, problems...)
	}
	//: the directory's markers.
	return set
}

// readPin parses one pin file's markers, reporting each malformed one.
func readPin(path string) (pinFile, []string) {
	pin := pinFile{name: filepath.Base(path)}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	//: a pin file that does not parse declares nothing anybody can trust.
	if err != nil {
		//: one problem, the parser's.
		return pin, []string{fmt.Sprintf("%s: unreadable pin file: %v", pin.name, err)}
	}
	var problems []string
	//: every comment of the file.
	for _, group := range f.Comments {
		//: each line of it.
		for _, c := range group.List {
			//: a package marker: the package comment's digest.
			if strings.HasPrefix(c.Text, packageMarkerPrefix) {
				digest, perr := parsePackageMarker(c.Text)
				//: a package marker in another shape is reported where it is.
				if perr != nil {
					problems = append(problems, fmt.Sprintf("%s:%d: malformed package marker: %v", pin.name, fset.Position(c.Pos()).Line, perr))
					continue
				}
				pin.pkgDoc = digest
				continue
			}
			//: only a marker line.
			if !strings.HasPrefix(c.Text, markerPrefix) {
				continue
			}
			line := fset.Position(c.Pos()).Line
			m, merr := parseMarker(c.Text)
			//: a marker in another shape is reported where it is.
			if merr != nil {
				problems = append(problems, fmt.Sprintf("%s:%d: malformed marker: %v", pin.name, line, merr))
				continue
			}
			m.file, m.line = pin.name, line
			pin.markers = append(pin.markers, m)
		}
	}
	//: the file's markers and its problems.
	return pin, problems
}

// parseMarker reads `// go:<id> <kind> <canonical signature>`.
func parseMarker(text string) (marker, error) {
	rest := strings.TrimPrefix(text, "// ")
	id, rest, ok := strings.Cut(rest, " ")
	//: an id alone declares no kind.
	if !ok {
		//: say what is missing.
		return marker{}, fmt.Errorf("%q has no kind", text)
	}
	kind, canonical, ok := strings.Cut(rest, " ")
	//: a kind alone declares no signature.
	if !ok || strings.TrimSpace(canonical) == "" {
		//: say what is missing.
		return marker{}, fmt.Errorf("%q has no signature", text)
	}
	//: the id must be one.
	if _, err := parseGoID(id); err != nil {
		//: the grammar's refusal.
		return marker{}, err
	}
	//: the kind must be one docs/api writes.
	if !slices.Contains(apiKinds, kind) {
		//: name it.
		return marker{}, fmt.Errorf("%q is no kind", kind)
	}
	canonical, doc := splitDocDigest(strings.TrimSpace(canonical))
	//: the marker.
	return marker{id: id, kind: kind, canonical: canonical, doc: doc}, nil
}

// splitDocDigest cuts the doc digest a marker ends with off its signature:
// " doc:" then 64 hex digits; a signature without one has no digest.
func splitDocDigest(sig string) (canonical, doc string) {
	before, digest, found := strings.CutLast(sig, markerDocPrefix)
	//: no digest, or not one at the end.
	if !found || !isDigest(digest) {
		//: the signature as it is.
		return sig, ""
	}
	//: the signature, then the digest.
	return before, digest
}

// parsePackageMarker reads `// package <path> doc:<sha256>`.
func parsePackageMarker(text string) (string, error) {
	path, digest, ok := strings.Cut(strings.TrimPrefix(text, packageMarkerPrefix), markerDocPrefix)
	//: a path, then a digest.
	if !ok || strings.TrimSpace(path) == "" || strings.Contains(path, " ") || !isDigest(digest) {
		//: say what it should be.
		return "", fmt.Errorf("%q is not `// package <path> doc:<sha256>`", text)
	}
	//: the digest.
	return digest, nil
}

// isDigest reports whether s is a sha256 in lower-case hex.
func isDigest(s string) bool {
	//: 64 digits.
	if len(s) != digestLen {
		//: too short or too long.
		return false
	}
	//: each one hex.
	for _, r := range s {
		//: a digit or a to f.
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			//: not hex.
			return false
		}
	}
	//: a digest.
	return true
}

// docDigest is the digest a marker carries for a symbol: its doc as docs/api
// records it, then each exported field's that has a doc, as NUL, the
// field's name, NUL, its doc.
func docDigest(s *apiSymbol) string {
	h := sha256.New()
	h.Write([]byte(s.Doc))
	//: a struct's fields, in order.
	for _, f := range s.Fields {
		//: a field with no doc adds nothing.
		if f.Doc == "" {
			continue
		}
		h.Write([]byte("\x00" + f.Name + "\x00" + f.Doc))
	}
	//: the digest in hex.
	return hex.EncodeToString(h.Sum(nil))
}

// textDigest is the digest of a package comment.
func textDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	//: in hex.
	return hex.EncodeToString(sum[:])
}

// cellMarkers are the markers the pin files a cell compiles declare, by id,
// with the ids declared twice on that cell.
func cellMarkers(set markerSet, dir string, cell platform) (byID map[string]marker, twice, pkgDocs []string) {
	byID = map[string]marker{}
	//: each pin file the go command compiles on this cell.
	for _, pin := range set.pins {
		//: a file whose header cannot be read is compiled nowhere.
		if ok, err := cell.matches(dir, pin.name); err != nil || !ok {
			continue
		}
		//: its package marker, when it has one.
		if pin.pkgDoc != "" {
			pkgDocs = append(pkgDocs, pin.pkgDoc)
		}
		//: each of its markers.
		for _, m := range pin.markers {
			//: one cell declares a symbol once.
			if _, dup := byID[m.id]; dup {
				twice = append(twice, m.id)
				continue
			}
			byID[m.id] = m
		}
	}
	//: the cell's markers.
	return byID, twice, pkgDocs
}

// finding is one difference between the code and the markers, on the cells
// it holds on.
type finding struct {
	// where names the package directory, relative to the root.
	where string
	// id is the symbol.
	id string
	// what says what differs.
	what string
}

// markerFindings compares, package by package and cell by cell, the code's
// symbols with the markers of the pin files each cell compiles, and returns
// each difference once, with the cells it holds on.
func markerFindings(byCell []map[string][]apiSymbol, pkgDocs []map[string]string, cells []platform, dirs []packageDir, root string) []string {
	var out []string
	//: each package directory of the project.
	for _, d := range dirs {
		set := readPins(d.dir)
		where := relSlash(root, d.dir)
		//: a malformed marker is a finding on every cell.
		for _, p := range set.problems {
			out = append(out, where+"/"+p)
		}
		seen := newFindingCells(len(cells))
		//: each cell judges the files it compiles.
		for i, cell := range cells {
			code := symbolsByID(byCell[i][d.dir])
			marks, twice, pkgMarks := cellMarkers(set, d.dir, cell)
			found := compareCell(d.path, code, marks, twice)
			//: the package comment, when the pins carry its digest.
			if len(pkgMarks) > 0 && i < len(pkgDocs) {
				found = append(found, comparePackageDoc(d.path, pkgDocs[i][d.dir], pkgMarks)...)
			}
			//: every difference on this cell.
			for _, f := range found {
				f.where = where
				seen.add(f, i)
			}
		}
		out = append(out, seen.lines(cells)...)
	}
	//: every finding.
	return out
}

// symbolsByID indexes one package's symbols on one cell.
func symbolsByID(syms []apiSymbol) map[string]apiSymbol {
	out := make(map[string]apiSymbol, len(syms))
	//: one cell holds one form of each id.
	for _, s := range syms {
		out[s.ID] = s
	}
	//: the index.
	return out
}

// compareCell compares one package's code with its markers on one cell.
func compareCell(pkgPath string, code map[string]apiSymbol, marks map[string]marker, twice []string) []finding {
	var out []finding
	//: an id declared twice on one cell.
	for _, id := range twice {
		out = append(out, finding{id: id, what: "declared twice by the pin markers one cell compiles"})
	}
	consumed := map[string]struct{}{}
	//: every symbol of the code, by id.
	for _, id := range sortedKeys(code) {
		s := code[id]
		out = append(out, judgeSymbol(&s, marks, consumed)...)
	}
	//: every marker no symbol of the code answered.
	for _, id := range sortedKeys(marks) {
		//: answered above.
		if _, ok := consumed[id]; ok {
			continue
		}
		out = append(out, finding{id: id, what: unansweredMarker(id, pkgPath)})
	}
	//: the cell's differences.
	return out
}

// judgeSymbol compares one symbol of the code with the marker of its id,
// consuming the marker it answers.
func judgeSymbol(s *apiSymbol, marks map[string]marker, consumed map[string]struct{}) []finding {
	m, ok := marks[s.ID]
	//: no marker of that id: a receiver flipped, or a symbol the design does
	//: not declare.
	if !ok {
		//: the same method with the other receiver.
		if flipped, fok := marks[flipReceiver(s.ID)]; fok {
			consumed[flipped.id] = struct{}{}
			//: one finding for the pair.
			return []finding{{id: s.ID, what: receiverFinding(s.ID)}}
		}
		//: added without the design.
		return []finding{{id: s.ID, what: "undeclared: the code declares it and no pin marker does — a symbol added without the design"}}
	}
	consumed[s.ID] = struct{}{}
	var out []finding
	//: a function turned variable, a type turned alias.
	if m.kind != s.Kind {
		out = append(out, finding{id: s.ID, what: fmt.Sprintf("kind differs: the code has a %s, the marker a %s", s.Kind, m.kind)})
	}
	//: a doc edited without the design, when the design owns the docs.
	if m.doc != "" && m.doc != docDigest(s) {
		out = append(out, finding{id: s.ID, what: "doc differs: the code's doc comment is not the design's — a doc is edited in the design, then kit gen (ADR 0167)"})
	}
	//: a signature that differs.
	if m.canonical != s.Canonical {
		out = append(out, finding{id: s.ID, what: signatureFinding(s.Canonical, m.canonical)})
	}
	//: the symbol's differences.
	return out
}

// comparePackageDoc compares a package's comment on a cell with the digest
// its pin files' package markers carry: one digest, the comment's.
func comparePackageDoc(pkgPath, doc string, marks []string) []finding {
	want := textDigest(doc)
	//: every pin file the cell compiles says the same.
	for _, m := range marks {
		//: one that does not.
		if m != want {
			//: one finding for the package.
			return []finding{{id: "go:" + pkgPath, what: "package comment differs: the code's is not the design's — it is edited in the design, then kit gen writes doc.go (ADR 0167)"}}
		}
	}
	//: the comment is the design's.
	return nil
}

// unansweredMarker says what a marker the code does not answer is: a symbol
// of this package the code lacks, or a symbol of another package.
func unansweredMarker(id, pkgPath string) string {
	parsed, err := parseGoID(id)
	//: a marker naming another package's symbol.
	if err == nil && parsed.path != pkgPath {
		//: say whose.
		return "foreign marker: it names a symbol of " + parsed.path + ", not of this package"
	}
	//: declared by the design, absent from the code.
	return "missing: a pin marker declares it and the code does not"
}

// flipReceiver is a method's id with its receiver turned from value to
// pointer or back; any other id unchanged.
func flipReceiver(id string) string {
	parsed, err := parseGoID(id)
	//: only a method has a receiver to flip.
	if err != nil || parsed.recv == "" {
		//: unchanged.
		return id
	}
	parsed.flags ^= idPointer
	//: the other receiver.
	return parsed.String()
}

// receiverFinding says which way a method's receiver changed.
func receiverFinding(codeID string) string {
	code := "value"
	design := "pointer"
	//: the code's id says which receiver it has.
	if parsed, err := parseGoID(codeID); err == nil && parsed.has(idPointer) {
		code, design = "pointer", "value"
	}
	//: the code's receiver against the marker's.
	return fmt.Sprintf("receiver differs: the code's method has a %s receiver, the marker's a %s one", code, design)
}

// signatureFinding names what differs between two canonical signatures: the
// type parameters' constraints, a struct tag, or the signature at large.
func signatureFinding(code, design string) string {
	what := "signature differs"
	//: the type parameter lists differ while the rest does not.
	if tc, rc := splitTypeParams(code); tc != "" {
		td, rd := splitTypeParams(design)
		//: only the constraints changed.
		if td != "" && tc != td && rc == rd {
			what = "constraint differs"
		}
	}
	//: the same signature once every quoted tag is removed.
	if what == "signature differs" && quotedRun.ReplaceAllString(code, `""`) == quotedRun.ReplaceAllString(design, `""`) {
		what = "tag differs"
	}
	//: the finding and both signatures.
	return fmt.Sprintf("%s: the code has %s, the marker %s", what, code, design)
}

// splitTypeParams splits a canonical signature's type parameter list from the
// rest: "[K comparable] struct{…}" and "func[K comparable](…)" both give
// "[K comparable]"; a signature with none gives "".
func splitTypeParams(sig string) (params, rest string) {
	open := strings.Index(sig, "[")
	//: a list opens the signature, or follows func.
	if open != 0 && !strings.HasPrefix(sig, "func[") {
		//: no list.
		return "", sig
	}
	depth := 0
	//: the bracket that closes the list, nesting counted.
	for i := open; i < len(sig); i++ {
		//: only brackets change the depth.
		switch sig[i] {
		//: one deeper.
		case '[':
			depth++
		//: one shallower; zero closes the list.
		case ']':
			depth--
			//: the list's end; a list of type parameters names their
			//: constraints, so it holds a space, unlike [] or [4].
			if depth == 0 && strings.Contains(sig[open:i+1], " ") {
				//: the list and what follows it, func kept in front.
				return sig[open : i+1], sig[:open] + sig[i+1:]
			}
			//: a slice or an array's brackets.
			if depth == 0 {
				//: no list.
				return "", sig
			}
		//: any other byte.
		default:
		}
	}
	//: an unbalanced list is no list.
	return "", sig
}

// findingCells aggregates findings across cells: one line per finding, with
// the cells it holds on.
type findingCells struct {
	// n is how many cells were judged.
	n int
	// cells are the cells of each finding, by finding.
	cells map[finding][]int
	// order is the findings in the order first seen.
	order []finding
}

// newFindingCells starts an empty aggregation over n cells.
func newFindingCells(n int) *findingCells {
	//: nothing seen yet.
	return &findingCells{n: n, cells: map[finding][]int{}}
}

// add records f on cell i.
func (a *findingCells) add(f finding, i int) {
	//: the first sighting keeps the order.
	if _, seen := a.cells[f]; !seen {
		a.order = append(a.order, f)
	}
	a.cells[f] = append(a.cells[f], i)
}

// lines renders each finding once: where, which symbol, what differs, and on
// which cells unless it holds on all.
func (a *findingCells) lines(cells []platform) []string {
	out := make([]string, 0, len(a.order))
	//: in the order first seen.
	for _, f := range a.order {
		on := ""
		//: a finding on some cells only says which.
		if got := a.cells[f]; len(got) < a.n {
			on = " (on " + strings.Join(platformsOf(got, cellNames(cells)), ", ") + ")"
		}
		out = append(out, fmt.Sprintf("%s: %s: %s%s", f.where, f.id, f.what, on))
	}
	//: the lines.
	return out
}
