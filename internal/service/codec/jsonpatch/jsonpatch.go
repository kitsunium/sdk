// Package jsonpatch computes the structural difference between two JSON
// documents as RFC 6902 operations — add, remove and replace, each at an
// RFC 6901 JSON Pointer — with the value each operation writes AND the value
// it replaces or removes (ADR 0143). Public facade: pkg/v1/codec/jsonpatch.
//
// It is what a record's history needs to say what changed between two of its
// versions: the operations are the edits, both values are what a reader shows,
// and applied in order to the first document they give the second.
//
// # How documents are compared
//
// Each document must be exactly one JSON value, read strictly: no duplicated
// member name, valid UTF-8, no trailing data. Values are compared as RFC 6902
// §4.6 compares them — strings once unescaped, numbers numerically (1, 1.0 and
// 1e0 are one number, compared exactly, never through a float), arrays element
// by element, objects by member name whatever their order.
//
// # What the operations are
//
//   - Two objects: a member only the first has is removed, one only the second
//     has is added, and one both have is compared in turn. Members come in
//     byte order of their names, so the same two documents always give the
//     same operations.
//   - Two arrays: the elements both keep, in order, are found — the longest
//     common subsequence of the elements that differ, after the equal ones at
//     both ends — and between two kept elements, those removed and those added
//     are paired in order and compared in turn, the rest removed or added. An
//     insertion in the middle is one add, not a rewrite of every element after
//     it. Indices are those of the array as the operations before have left
//     it, as RFC 6902 applies them.
//   - Anything else that differs — another kind, another string, another number
//     — is replaced whole; two roots of different kinds are one replace at "".
//
// # What it is not
//
// It emits no move, copy or test, and does not apply a patch. A value is the
// JSON of that part of the document, compact; a number keeps its spelling.
// Nothing it returns in an error comes from a document.
package jsonpatch

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// maxAlignCells bounds the table the alignment of two arrays' differing
// middles builds: past it, their elements are paired by position instead,
// which is still a correct patch — only a longer one.
const maxAlignCells int = 1 << 18

// Op is what an operation does, as RFC 6902 names it.
type Op string

// The three operations a diff emits.
const (
	// Add adds a member to an object, or inserts an element into an array
	// before the one at the index — after every element when the index is
	// the array's length.
	Add Op = "add"
	// Remove removes a member of an object, or the element at the index.
	Remove Op = "remove"
	// Replace replaces the value at the path, the whole document at "".
	Replace Op = "replace"
)

// EditValue is one operation of a diff: what it does, where, and both values.
// Its JSON is an RFC 6902 operation, with the value it replaces or removes
// under "old" — a member RFC 6902 does not define for these operations, which
// a patch applier ignores (§4) — so a list of them is a JSON Patch document.
type EditValue struct {
	// Op is add, remove or replace.
	Op Op `json:"op"`
	// Path is the RFC 6901 JSON Pointer of the value the operation changes.
	Path string `json:"path"`
	// Value is the value the operation writes: add and replace; absent for
	// remove.
	Value json.RawMessage `json:"value,omitempty"`
	// Old is the value the operation replaces or removes: replace and
	// remove; absent for add.
	Old json.RawMessage `json:"old,omitempty"`
}

// Diff returns the operations that turn the JSON document from into the JSON
// document to, in the order they apply: none — an empty slice — when the two
// are the same value. It returns NotJSON, naming the document and the offset,
// when either is not exactly one JSON value.
func Diff(from, to []byte) ([]EditValue, error) {
	a, err := parse("from", from)
	//: NotJSON.
	if err != nil {
		//: nothing compared.
		return nil, err
	}
	b, err := parse("to", to)
	//: NotJSON.
	if err != nil {
		//: nothing compared.
		return nil, err
	}
	var d differ
	d.diff("", a, b)
	//: never nil.
	return append([]EditValue{}, d.edits...), nil
}

// differ collects the operations of one diff.
type differ struct {
	// edits are the operations so far, in order.
	edits []EditValue
}

// diff appends the operations that turn a, at path, into b.
func (d *differ) diff(path string, a, b *node) {
	switch {
	//: nothing changed here.
	case equal(a, b):
		return
	//: two objects: member by member.
	case a.kind == '{' && b.kind == '{':
		d.diffObjects(path, a, b)
	//: two arrays: aligned, then element by element.
	case a.kind == '[' && b.kind == '[':
		d.diffArrays(path, a.items, b.items)
	//: another kind, or another scalar: the whole value.
	default:
		d.edits = append(d.edits, EditValue{Op: Replace, Path: path, Value: encode(b), Old: encode(a)})
	}
}

// diffObjects appends the operations that turn object a, at path, into object
// b: members in byte order of their names, removed, compared or added.
func (d *differ) diffObjects(path string, a, b *node) {
	names := append(slices.Clone(a.names), b.names...)
	slices.Sort(names)
	//: each name once, in order.
	for _, name := range slices.Compact(names) {
		before, inA := a.fields[name]
		after, inB := b.fields[name]
		at := path + "/" + escapeToken(name)
		switch {
		//: in both: compared in turn.
		case inA && inB:
			d.diff(at, before, after)
		//: only in the first: removed.
		case inA:
			d.edits = append(d.edits, EditValue{Op: Remove, Path: at, Old: encode(before)})
		//: only in the second: added.
		default:
			d.edits = append(d.edits, EditValue{Op: Add, Path: at, Value: encode(after)})
		}
	}
}

// diffArrays appends the operations that turn array a, at path, into array b.
// The equal elements at both ends are kept; the elements of the middles both
// keep are found by alignment; between two kept elements, the removed and the
// added are paired in order and compared, the rest removed or added. i is the
// index in the array as the operations so far have left it.
func (d *differ) diffArrays(path string, a, b []*node) {
	prefix := commonPrefix(a, b)
	suffix := commonPrefix(reversed(a[prefix:]), reversed(b[prefix:]))
	midA, midB := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	i, x, y := prefix, 0, 0
	//: each kept element closes the run of changes before it; the last run
	//: is closed by the end of the middles.
	for _, kept := range append(align(midA, midB), [2]int{len(midA), len(midB)}) {
		i = d.changeRun(path, i, midA[x:kept[0]], midB[y:kept[1]])
		x, y = kept[0]+1, kept[1]+1
		i++
	}
}

// changeRun appends the operations that turn the removed elements into the
// added ones, starting at index i, and returns the index after them: pairs
// compared in turn, then the extra ones removed or added.
func (d *differ) changeRun(path string, i int, removed, added []*node) int {
	paired := min(len(removed), len(added))
	//: the pairs, compared in place.
	for k := range paired {
		d.diff(path+"/"+strconv.Itoa(i), removed[k], added[k])
		i++
	}
	//: the removed ones left over, each at the same index as it closes up.
	for _, gone := range removed[paired:] {
		d.edits = append(d.edits, EditValue{Op: Remove, Path: path + "/" + strconv.Itoa(i), Old: encode(gone)})
	}
	//: the added ones left over, in order.
	for _, came := range added[paired:] {
		d.edits = append(d.edits, EditValue{Op: Add, Path: path + "/" + strconv.Itoa(i), Value: encode(came)})
		i++
	}
	//: the index after the run.
	return i
}

// align returns the pairs of indices, into a and into b, of the elements both
// keep in order — a longest common subsequence under equal — ascending. Past
// maxAlignCells it keeps none, and the elements are paired by position.
func align(a, b []*node) [][2]int {
	//: nothing to align, or too much to align — divided rather than
	//: multiplied, since two long arrays' product overflows a 32-bit int.
	if len(a) == 0 || len(b) == 0 || len(a) > maxAlignCells/len(b) {
		//: none kept.
		return nil
	}
	lengths, width := lcsLengths(a, b), len(b)+1
	var kept [][2]int
	i, j := 0, 0
	//: the pairs, read forwards.
	for i < len(a) && j < len(b) {
		switch {
		//: kept.
		case equal(a[i], b[j]):
			kept = append(kept, [2]int{i, j})
			i, j = i+1, j+1
		//: skipping a's element loses nothing.
		case lengths[(i+1)*width+j] >= lengths[i*width+j+1]:
			i++
		//: skipping b's element loses nothing.
		default:
			j++
		}
	}
	//: ascending in both.
	return kept
}

// lcsLengths returns the table of the longest common subsequences of the
// suffixes of a and b: the entry at i*(len(b)+1)+j is the length for a[i:]
// and b[j:].
func lcsLengths(a, b []*node) []uint32 {
	width := len(b) + 1
	lengths := make([]uint32, (len(a)+1)*width)
	//: from the ends back.
	for i, x := range slices.Backward(a) {
		//: every suffix of b.
		for j, y := range slices.Backward(b) {
			//: kept, or the better of skipping either.
			if equal(x, y) {
				lengths[i*width+j] = lengths[(i+1)*width+j+1] + 1
				continue
			}
			lengths[i*width+j] = max(lengths[(i+1)*width+j], lengths[i*width+j+1])
		}
	}
	//: the table.
	return lengths
}

// commonPrefix returns how many elements a and b share at their start.
func commonPrefix(a, b []*node) int {
	n := 0
	//: while they agree.
	for n < len(a) && n < len(b) && equal(a[n], b[n]) {
		n++
	}
	//: the shared length.
	return n
}

// reversed returns s's elements in reverse order, in a new slice.
func reversed(s []*node) []*node {
	out := slices.Clone(s)
	slices.Reverse(out)
	//: the copy, reversed.
	return out
}

// escapeToken escapes a member name as an RFC 6901 reference token: "~" as
// "~0" first, then "/" as "~1", so "~1" in a name stays itself.
func escapeToken(name string) string {
	//: the order RFC 6901 §4 requires.
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
