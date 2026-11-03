// Package yaml_test — the YAML project's own conformance suite,
// yaml-test-suite data-2022-01-17 (testdata/yaml-test-suite, MIT, copied byte
// for byte from the upstream data tag; README.md there says which files),
// run against the subset reader (#1107).
package yaml_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// suiteDir is the copy of the suite's data tag.
const suiteDir = "testdata/yaml-test-suite"

// The counts the run must reach: CLAUDE.md states them, and a refresh or a
// change of the reader that moves one fails here rather than silently.
const (
	// wantCases is every case of the suite: a test ID, or one of its numbered
	// sub-cases.
	wantCases = 402
	// wantEqual is the valid cases decoded to the value of their in.json.
	wantEqual = 143
	// wantNamed is the valid cases refused by a named subset sentinel.
	wantNamed = 102
	// wantInvalid is the invalid cases, every one refused.
	wantInvalid = 94
	// wantSkipped is the valid cases a named group below sets aside.
	wantSkipped = 63
)

// namedRefusals are the sentinels the subset refuses a YAML construct with,
// by name (CLAUDE.md, "Refused BY NAME").
var namedRefusals = []*errs.Error{
	coreyaml.AnchorRefused, coreyaml.AliasRefused, coreyaml.TagRefused,
	coreyaml.MergeKeyRefused, coreyaml.MultipleDocumentsRefused, coreyaml.ComplexKeyRefused,
	coreyaml.DirectiveRefused, coreyaml.DuplicateKey, coreyaml.LeadingZeroRefused,
}

// subsetLimits are the skipped groups of valid cases the subset refuses with
// UNMARSHAL_FAILED and a constant detail, keyed by that detail, with the
// reason: each is a construct CLAUDE.md lists outside the subset. A valid case
// refused with any other detail fails the run.
var subsetLimits = map[string]string{
	"content on the document start line is outside the supported subset":       "content on the `---` line is outside the subset (CLAUDE.md)",
	"a plain scalar cannot start with an indicator character":                  "a plain scalar opening with ':', '?' or '-' and a non-blank is outside the subset",
	"a flow collection entry must be followed by ',' or its closing indicator": "a flow key whose ':' is on a later line, or a multi-line flow key, is outside the subset",
	"a tab character cannot indent a line":                                     "a tab where indentation is is outside the subset (CLAUDE.md)",
	"a tab character cannot indent a block scalar":                             "a tab where a block scalar's indentation is is outside the subset (CLAUDE.md)",
	"a tab character cannot follow a sequence entry's '-'":                     "libyaml refuses it, and the subset reads nothing libyaml refuses",
	"a mapping key must be written on one line":                                "a multi-line implicit key is outside the subset (CLAUDE.md)",
	"a document end marker closes no document":                                 "a `...` closing nothing is outside the subset (CLAUDE.md)",
	"a plain scalar in a flow collection cannot hold '?': quote it":            "a '?' inside a plain scalar in flow context is outside the subset",
	"an unknown escape sequence in a double-quoted scalar":                     "the `\\/` escape is outside the subset (CLAUDE.md)",
}

// readingGroups are the skipped groups of valid cases the subset reads, but
// not as the suite does, by name, with the reason and the cases.
var readingGroups = map[string]struct {
	reason string
	cases  []string
}{
	"a block scalar the input ends inside, on a line of blanks": {
		reason: "yaml.v3, which the subset replaced and FuzzNativeAgreesWithYAMLv3 holds it to, ends the last line without the line break the suite's reference parser adds at the end of the input; reading it the suite's way would change the value of every document ending in a block scalar without a final newline",
		cases:  []string{"JEF9/02", "L24T/01"},
	},
	"no in.json": {
		reason: "the suite gives no JSON form to compare with: the case is decoded, not compared",
		cases:  []string{"4ABK"},
	},
}

// suiteCase is one case of the suite.
type suiteCase struct {
	id      string
	in      []byte
	json    []byte
	invalid bool
}

// suiteCounts is what one run checked.
type suiteCounts struct {
	equal, named, invalid, skipped int
}

// readSuite returns every case of the suite, in ID order.
func readSuite(t *testing.T) []suiteCase {
	t.Helper()
	var cases []suiteCase
	err := fs.WalkDir(os.DirFS(suiteDir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() != "in.yaml" {
			return err
		}
		dir := path.Dir(p)
		c := suiteCase{id: dir}
		if c.in, err = os.ReadFile(path.Join(suiteDir, p)); err != nil {
			return err
		}
		//: an absent in.json reads as no document.
		c.json, _ = os.ReadFile(path.Join(suiteDir, dir, "in.json"))
		_, err = os.Stat(path.Join(suiteDir, dir, "error"))
		c.invalid = err == nil
		cases = append(cases, c)
		return nil
	})
	if err != nil || len(cases) != wantCases {
		t.Fatalf("read suite: %v (%d cases, want %d)", err, len(cases), wantCases)
	}
	slices.SortFunc(cases, func(a, b suiteCase) int { return strings.Compare(a.id, b.id) })
	return cases
}

// TestYAMLTestSuite is #1107: every case of the yaml-test-suite either
// decodes to its in.json value, is refused by a named subset sentinel, is an
// invalid case refused, or sits in a named group with its reason — and no
// invalid case is accepted.
func TestYAMLTestSuite(t *testing.T) {
	t.Parallel()
	skipped := map[string]string{}
	for name, group := range readingGroups {
		for _, id := range group.cases {
			skipped[id] = name
		}
	}
	var counts suiteCounts
	for _, c := range readSuite(t) {
		checkCase(t, c, skipped, &counts)
	}
	if counts.equal != wantEqual || counts.named != wantNamed || counts.invalid != wantInvalid || counts.skipped != wantSkipped {
		t.Errorf("ran %d equal, %d refused by name, %d invalid refused, %d skipped; want %d, %d, %d, %d",
			counts.equal, counts.named, counts.invalid, counts.skipped, wantEqual, wantNamed, wantInvalid, wantSkipped)
	}
	t.Logf("yaml-test-suite: %d cases — %d decoded equal to in.json, %d refused by a named subset code, %d invalid refused, 0 invalid accepted, %d skipped in %d named groups",
		wantCases, counts.equal, counts.named, counts.invalid, counts.skipped, len(subsetLimits)+len(readingGroups))
}

// checkCase runs one case and counts its outcome.
func checkCase(t *testing.T, c suiteCase, skipped map[string]string, counts *suiteCounts) {
	t.Helper()
	var v any
	err := yaml.New().Unmarshal(c.in, &v)
	switch {
	case c.invalid && err == nil:
		t.Errorf("%s: an invalid case was accepted as %#v", c.id, v)
	case c.invalid:
		counts.invalid++
	case skipped[c.id] != "":
		counts.skipped++
	case err != nil:
		checkRefusal(t, c, err, counts)
	case !sameAsJSON(v, c.json):
		t.Errorf("%s: decoded %#v, in.json holds %s", c.id, v, bytes.TrimSpace(c.json))
	default:
		counts.equal++
	}
}

// checkRefusal accepts the refusal of a valid case when a named sentinel
// made it, or a detail subsetLimits names.
func checkRefusal(t *testing.T, c suiteCase, err error, counts *suiteCounts) {
	t.Helper()
	for _, sentinel := range namedRefusals {
		if errors.Is(err, sentinel) {
			counts.named++
			return
		}
	}
	detail := detailOf(err)
	if _, ok := subsetLimits[detail]; ok {
		counts.skipped++
		return
	}
	t.Errorf("%s: a valid case refused with no named reason: %v (detail %q)", c.id, err, detail)
}

// detailOf is the detail field a syntax refusal carries.
func detailOf(err error) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "detail" {
			return f.StringValue()
		}
	}
	return ""
}

// sameAsJSON reports whether v, read through encoding/json, is the one value
// in.json holds — nothing for an empty stream.
func sameAsJSON(v any, inJSON []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(inJSON))
	var docs []any
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return v == nil
	}
	encoded, err := json.Marshal(v)
	if err != nil || len(docs) != 1 {
		return false
	}
	var got any
	if err := json.Unmarshal(encoded, &got); err != nil {
		return false
	}
	return reflect.DeepEqual(got, docs[0])
}
