// Package bson_test — the MongoDB specifications' own BSON corpus,
// bson-corpus (testdata/bson-corpus, CC BY-NC-SA 3.0 US, copied byte for
// byte from mongodb/specifications; README.md there carries the attribution),
// run against the decoder and the encoder (#1107).
package bson_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/bson"
)

// corpusDir is the copy of bson-corpus's tests/ directory.
const corpusDir = "testdata/bson-corpus"

// The corpus counts the run must reach: a refresh that loses a file or a case
// fails here rather than shrinking the suite silently.
const (
	// wantValid is the valid cases, each canonical_bson round-tripped.
	wantValid = 731
	// wantDecodeErrors is the decodeErrors cases, each refused.
	wantDecodeErrors = 75
	// wantDecimalParseErrors is the decimal128 parseErrors cases, each refused
	// by ParseDecimal128.
	wantDecimalParseErrors = 131
)

// relaxations names the valid cases the codec deliberately reads otherwise,
// by "file: description", with the reason — the CLAUDE.md "Intentional"
// list. Empty: every valid case round-trips.
var relaxations = map[string]string{}

// skippedGroups are the corpus groups the codec has nothing to run against,
// by name, with the reason.
var skippedGroups = map[string]string{
	"parseErrors outside decimal128":        "their inputs are extended JSON, which the codec does not port (CLAUDE.md: extended JSON not ported)",
	"canonical_extjson and relaxed_extjson": "extended JSON is not ported; only decimal128's $numberDecimal text is compared, against Decimal128.String",
	"converted_bson":                        "the conversion of deprecated types the corpus describes is a driver's choice; the codec keeps them as they are",
}

// corpusFile is one bson-corpus file.
type corpusFile struct {
	// Description names the type.
	Description string `json:"description"`
	// BSONType is the element type byte under test, "0x13" for decimal128.
	BSONType string `json:"bson_type"`
	// TestKey is the element name under test.
	TestKey string `json:"test_key"`
	// Valid are the documents to round-trip.
	Valid []corpusValid `json:"valid"`
	// DecodeErrors are the documents to refuse.
	DecodeErrors []corpusError `json:"decodeErrors"`
	// ParseErrors are the strings to refuse: decimal text for 0x13.
	ParseErrors []corpusParseError `json:"parseErrors"`
}

// corpusValid is one valid case.
type corpusValid struct {
	// Description names the case.
	Description string `json:"description"`
	// CanonicalBSON is the document, hexadecimal.
	CanonicalBSON string `json:"canonical_bson"`
	// DegenerateBSON is a non-canonical form that decodes to the same value.
	DegenerateBSON string `json:"degenerate_bson"`
	// CanonicalExtJSON is the document as canonical extended JSON.
	CanonicalExtJSON string `json:"canonical_extjson"`
	// Lossy marks a case whose extended JSON loses information.
	Lossy bool `json:"lossy"`
}

// corpusError is one decodeErrors case.
type corpusError struct {
	// Description names the case.
	Description string `json:"description"`
	// BSON is the document, hexadecimal.
	BSON string `json:"bson"`
}

// corpusParseError is one parseErrors case.
type corpusParseError struct {
	// Description names the case.
	Description string `json:"description"`
	// String is the input the parser must refuse.
	String string `json:"string"`
}

// corpusCounts is what one run checked.
type corpusCounts struct {
	valid, degenerate, decodeErrors, parseErrors, skippedParse int
}

// readCorpus reads every file of the corpus, in name order.
func readCorpus(t *testing.T) map[string]corpusFile {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	//: the corpus ships with the package.
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	files := map[string]corpusFile{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(path.Join(corpusDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var f corpusFile
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files[e.Name()] = f
	}
	//: an empty corpus would pass every check.
	if len(files) != 31 {
		t.Fatalf("corpus holds %d files, want 31", len(files))
	}
	return files
}

// mustHex decodes a corpus hexadecimal field.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestCorpus is #1107: every valid case's canonical_bson decodes into the
// generic document type and encodes back to the same bytes, its
// degenerate_bson to the canonical bytes; every decodeErrors document is
// refused with a BSON code; every decimal128 parseErrors string is refused by
// ParseDecimal128; and a decimal128 value prints as its extended JSON text.
func TestCorpus(t *testing.T) {
	t.Parallel()
	files := readCorpus(t)
	var counts corpusCounts
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range files {
			if !yield(name) {
				return
			}
		}
	})
	for _, name := range names {
		f := files[name]
		for _, v := range f.Valid {
			checkValid(t, name, f, v, &counts)
		}
		for _, e := range f.DecodeErrors {
			checkDecodeError(t, name, e)
			counts.decodeErrors++
		}
		checkParseErrors(t, name, f, &counts)
	}
	if counts.valid != wantValid || counts.decodeErrors != wantDecodeErrors || counts.parseErrors != wantDecimalParseErrors {
		t.Errorf("ran %d valid, %d decodeErrors, %d decimal parseErrors; want %d, %d, %d",
			counts.valid, counts.decodeErrors, counts.parseErrors, wantValid, wantDecodeErrors, wantDecimalParseErrors)
	}
	if len(relaxations) != 0 {
		t.Errorf("relaxations must stay empty unless CLAUDE.md names each case: %v", relaxations)
	}
	t.Logf("bson-corpus: %d valid round-trips (%d degenerate), %d decodeErrors refused, %d decimal parseErrors refused, %d extended-JSON parseErrors skipped (%d groups skipped by name)",
		counts.valid, counts.degenerate, counts.decodeErrors, counts.parseErrors, counts.skippedParse, len(skippedGroups))
}

// checkValid round-trips one valid case.
func checkValid(t *testing.T, name string, f corpusFile, v corpusValid, counts *corpusCounts) {
	t.Helper()
	counts.valid++
	id := name + ": " + v.Description
	if reason, ok := relaxations[id]; ok {
		t.Logf("%s: relaxed — %s", id, reason)
		return
	}
	canonical := mustHex(t, v.CanonicalBSON)
	var doc bson.D
	if err := bson.New().Unmarshal(canonical, &doc); err != nil {
		t.Errorf("%s: Unmarshal(canonical_bson) = %v", id, err)
		return
	}
	if got, err := bson.New().Marshal(doc); err != nil || !bytes.Equal(got, canonical) {
		t.Errorf("%s: Marshal(Unmarshal(canonical_bson)) = %X, %v; want %s", id, got, err, v.CanonicalBSON)
	}
	if f.BSONType == "0x13" {
		checkDecimalText(t, id, f.TestKey, doc, v)
	}
	if v.DegenerateBSON == "" {
		return
	}
	counts.degenerate++
	var degenerate bson.D
	if err := bson.New().Unmarshal(mustHex(t, v.DegenerateBSON), &degenerate); err != nil {
		t.Errorf("%s: Unmarshal(degenerate_bson) = %v", id, err)
		return
	}
	if got, err := bson.New().Marshal(degenerate); err != nil || !bytes.Equal(got, canonical) {
		t.Errorf("%s: Marshal(Unmarshal(degenerate_bson)) = %X, %v; want %s", id, got, err, v.CanonicalBSON)
	}
}

// checkDecimalText compares a decoded decimal128 with the $numberDecimal
// text of the case's canonical extended JSON, unless the case is lossy.
func checkDecimalText(t *testing.T, id, key string, doc bson.D, v corpusValid) {
	t.Helper()
	if v.Lossy {
		return
	}
	var ext map[string]map[string]string
	if err := json.Unmarshal([]byte(v.CanonicalExtJSON), &ext); err != nil {
		t.Errorf("%s: canonical_extjson: %v", id, err)
		return
	}
	want := ext[key]["$numberDecimal"]
	for _, e := range doc {
		if e.Key != key {
			continue
		}
		d, ok := e.Value.(bson.Decimal128)
		if !ok || d.String() != want {
			t.Errorf("%s: Decimal128 = %v, want %s", id, e.Value, want)
		}
		return
	}
	t.Errorf("%s: no element %q", id, key)
}

// checkDecodeError requires one decodeErrors document to be refused with a
// code of the codec's range.
func checkDecodeError(t *testing.T, name string, e corpusError) {
	t.Helper()
	var doc bson.D
	err := bson.New().Unmarshal(mustHex(t, e.BSON), &doc)
	if err == nil {
		t.Errorf("%s: %s: decoded, want a refusal", name, e.Description)
		return
	}
	if code, ok := errs.CodeOf(err); !ok || code>>8 != 0x00_03_24 {
		t.Errorf("%s: %s: refused with %v, want a 0.3.36.* code", name, e.Description, err)
	}
}

// checkParseErrors refuses decimal128's parseErrors strings through
// ParseDecimal128 and counts every other file's, which are extended JSON.
func checkParseErrors(t *testing.T, name string, f corpusFile, counts *corpusCounts) {
	t.Helper()
	if f.BSONType != "0x13" {
		counts.skippedParse += len(f.ParseErrors)
		return
	}
	for _, p := range f.ParseErrors {
		counts.parseErrors++
		if d, err := bson.ParseDecimal128(p.String); err == nil {
			t.Errorf("%s: %s: ParseDecimal128(%q) = %v, want a refusal", name, p.Description, p.String, d)
		}
	}
}
