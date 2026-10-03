// Package toml_test — the TOML project's own conformance suite, toml-test
// v2.1.0 (testdata/toml-test, MIT, copied byte for byte from the upstream
// tag's tests/ directory), run against the decoder and the encoder.
package toml_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/toml"
)

// corpusDir is the copy of toml-test's tests/ directory.
const corpusDir = "testdata/toml-test"

// relaxations are the documents TOML v1.0.0 declares invalid and TOML v1.1.0
// made valid, which the codec accepts because the library it replaced
// accepted them: newlines and a trailing comma in an inline table, the \x
// escape, and a time without seconds. Every other v1.0.0-invalid document is
// refused.
var relaxations = map[string]bool{
	"invalid/datetime/no-secs.toml":            true,
	"invalid/local-datetime/no-secs.toml":      true,
	"invalid/local-time/no-secs.toml":          true,
	"invalid/inline-table/linebreak-01.toml":   true,
	"invalid/inline-table/linebreak-02.toml":   true,
	"invalid/inline-table/linebreak-03.toml":   true,
	"invalid/inline-table/linebreak-04.toml":   true,
	"invalid/inline-table/trailing-comma.toml": true,
	"invalid/string/basic-byte-escapes.toml":   true,
}

// datetimeLayouts are the layouts toml-test compares each date and time kind
// with.
var datetimeLayouts = map[string]string{
	"datetime":       time.RFC3339Nano,
	"datetime-local": "2006-01-02T15:04:05.999999999",
	"date-local":     "2006-01-02",
	"time-local":     "15:04:05",
}

// datetimeSpelling normalises the spellings RFC 3339 allows before a parse.
var datetimeSpelling = strings.NewReplacer(" ", "T", "t", "T", "z", "Z")

// corpusList returns the files a version's list names.
func corpusList(t *testing.T, version string) []string {
	t.Helper()
	f, err := os.Open(path.Join(corpusDir, "files-toml-"+version))
	//: the list ships with the corpus.
	if err != nil {
		t.Fatalf("open list: %v", err)
	}
	defer func() {
		//: a read-only file; the close cannot lose data.
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close list: %v", cerr)
		}
	}()
	var files []string
	sc := bufio.NewScanner(f)
	//: one path per line.
	for sc.Scan() {
		//: only the TOML inputs; the expectations sit beside them.
		if line := sc.Text(); strings.HasSuffix(line, ".toml") {
			files = append(files, line)
		}
	}
	//: a broken list would silently empty the suite.
	if err := sc.Err(); err != nil || len(files) == 0 {
		t.Fatalf("read list: %v (%d files)", err, len(files))
	}
	//: the inputs.
	return files
}

// readCorpus returns one corpus file.
func readCorpus(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(path.Join(corpusDir, name))
	//: every listed file is in the corpus.
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	//: the bytes.
	return data
}

// tagged converts a decoded document to toml-test's tagged JSON shape.
func tagged(v any) any {
	switch x := v.(type) {
	//: a table.
	case map[string]any:
		out := make(map[string]any, len(x))
		//: each key.
		for k, e := range x {
			out[k] = tagged(e)
		}
		return out
	//: an array.
	case []any:
		out := make([]any, len(x))
		//: each element.
		for i, e := range x {
			out[i] = tagged(e)
		}
		return out
	//: a scalar.
	default:
		return taggedScalar(v)
	}
}

// taggedScalar converts one decoded scalar to its tagged form.
func taggedScalar(v any) map[string]any {
	var kind, text string
	switch x := v.(type) {
	case string:
		kind, text = "string", x
	case int64:
		kind, text = "integer", strconv.FormatInt(x, 10)
	case float64:
		kind, text = "float", strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		kind, text = "bool", strconv.FormatBool(x)
	case time.Time:
		kind, text = "datetime", x.Format(time.RFC3339Nano)
	case toml.LocalDateTime:
		kind, text = "datetime-local", x.String()
	case toml.LocalDate:
		kind, text = "date-local", x.String()
	case toml.LocalTime:
		kind, text = "time-local", x.String()
	default:
		kind, text = "unexpected", reflect.TypeOf(v).String()
	}
	//: {"type": ..., "value": ...}.
	return map[string]any{"type": kind, "value": text}
}

// sameTagged compares two tagged documents as toml-test compares them:
// floats and date-times by value, everything else exactly.
func sameTagged(want, have any) bool {
	switch w := want.(type) {
	//: a table, or a tagged scalar.
	case map[string]any:
		h, ok := have.(map[string]any)
		//: a scalar compares by its kind's rule.
		if ok && isScalarTag(w) && isScalarTag(h) {
			return sameScalar(w, h)
		}
		//: a table compares key by key.
		return ok && sameTables(w, h)
	//: an array compares element by element.
	case []any:
		h, ok := have.([]any)
		//: the same length first.
		if !ok || len(w) != len(h) {
			return false
		}
		//: each element.
		for i := range w {
			//: one mismatch is enough.
			if !sameTagged(w[i], h[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// sameTables compares two tagged tables key by key.
func sameTables(want, have map[string]any) bool {
	//: the same keys.
	if len(want) != len(have) {
		return false
	}
	//: each key.
	for k, wv := range want {
		hv, ok := have[k]
		//: missing, or different.
		if !ok || !sameTagged(wv, hv) {
			return false
		}
	}
	return true
}

// isScalarTag reports whether m is {"type": string, "value": string}.
func isScalarTag(m map[string]any) bool {
	_, typed := m["type"].(string)
	_, valued := m["value"].(string)
	//: exactly the two members, both strings.
	return len(m) == 2 && typed && valued
}

// sameScalar compares two tagged scalars.
func sameScalar(want, have map[string]any) bool {
	kind, _ := want["type"].(string)
	w, _ := want["value"].(string)
	h, _ := have["value"].(string)
	//: the kinds must agree first.
	if kind != have["type"] {
		return false
	}
	switch kind {
	case "float":
		return sameFloat(w, h)
	case "integer":
		wi, werr := strconv.ParseInt(w, 10, 64)
		hi, herr := strconv.ParseInt(h, 10, 64)
		return werr == nil && herr == nil && wi == hi
	case "datetime", "datetime-local", "date-local", "time-local":
		wt, werr := time.Parse(datetimeLayouts[kind], datetimeSpelling.Replace(w))
		ht, herr := time.Parse(datetimeLayouts[kind], datetimeSpelling.Replace(h))
		return werr == nil && herr == nil && wt.Equal(ht)
	default:
		return w == h
	}
}

// sameFloat compares two float spellings by value, NaN equal to NaN.
func sameFloat(want, have string) bool {
	want, have = strings.TrimLeft(strings.ToLower(want), "+"), strings.TrimLeft(strings.ToLower(have), "+")
	//: NaN has no value to compare, and its sign none either.
	if strings.HasSuffix(want, "nan") || strings.HasSuffix(have, "nan") {
		return strings.TrimLeft(want, "-") == strings.TrimLeft(have, "-")
	}
	wf, werr := strconv.ParseFloat(want, 64)
	hf, herr := strconv.ParseFloat(have, 64)
	return werr == nil && herr == nil && (wf == hf || (math.IsInf(wf, 0) && math.IsInf(hf, 0) && wf == hf))
}

// TestTOMLTestValid decodes every valid document of TOML v1.0.0 and v1.1.0
// and compares it with the suite's expectation.
func TestTOMLTestValid(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	//: both versions' valid documents, once each.
	for _, version := range []string{"1.0.0", "1.1.0"} {
		//: each listed document.
		for _, name := range corpusList(t, version) {
			//: invalid documents have their own test; duplicates run once.
			if !strings.HasPrefix(name, "valid/") || seen[name] {
				continue
			}
			seen[name] = true
			doc := readCorpus(t, name)
			var want any
			//: the expectation sits beside the document.
			if err := json.Unmarshal(readCorpus(t, strings.TrimSuffix(name, ".toml")+".json"), &want); err != nil {
				t.Fatalf("%s: expectation: %v", name, err)
			}
			var got map[string]any
			//: a valid document decodes.
			if err := toml.New().Unmarshal(doc, &got); err != nil {
				t.Errorf("%s: refused: %v %v", name, err, errs.FieldsOf(err))
				continue
			}
			//: to exactly what the suite expects.
			if have := tagged(got); !sameTagged(want, have) {
				t.Errorf("%s: decoded value differs\nwant %v\nhave %v", name, want, have)
			}
		}
	}
	//: the suite really ran.
	if len(seen) < 200 {
		t.Fatalf("only %d valid documents ran", len(seen))
	}
}

// TestTOMLTestInvalid decodes every document TOML v1.0.0 and v1.1.0 declare
// invalid: each is refused with UNMARSHAL_FAILED, except the documents that
// TOML v1.1.0 relaxed, which are accepted.
func TestTOMLTestInvalid(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	accepted := 0
	//: both versions' invalid documents, once each.
	for _, version := range []string{"1.0.0", "1.1.0"} {
		//: each listed document.
		for _, name := range corpusList(t, version) {
			//: valid documents have their own test; duplicates run once.
			if !strings.HasPrefix(name, "invalid/") || seen[name] {
				continue
			}
			seen[name] = true
			var got map[string]any
			err := toml.New().Unmarshal(readCorpus(t, name), &got)
			//: a relaxation decodes.
			if relaxations[name] {
				accepted++
				//: accepted, as the replaced library accepted it.
				if err != nil {
					t.Errorf("%s: a TOML v1.1.0 relaxation was refused: %v %v", name, err, errs.FieldsOf(err))
				}
				continue
			}
			//: every other invalid document is refused, as UNMARSHAL_FAILED.
			if !errs.HasReason(err, "UNMARSHAL_FAILED") {
				t.Errorf("%s: want UNMARSHAL_FAILED, got %v (decoded %v)", name, err, got)
			}
		}
	}
	//: the relaxations really are in the corpus, and the suite really ran.
	if accepted != 9 || len(seen) < 450 {
		t.Fatalf("ran %d invalid documents, %d relaxations accepted, want 9", len(seen), accepted)
	}
}

// TestTOMLTestRoundTrip encodes every valid document's decoded value and
// decodes it back: the encoder writes what the decoder reads, value for value.
func TestTOMLTestRoundTrip(t *testing.T) {
	t.Parallel()
	ran := 0
	//: the v1.1.0 list is a superset of the v1.0.0 valid documents.
	for _, name := range corpusList(t, "1.1.0") {
		//: only valid documents have a value to encode.
		if !strings.HasPrefix(name, "valid/") {
			continue
		}
		ran++
		var first map[string]any
		//: the corpus document.
		if err := toml.New().Unmarshal(readCorpus(t, name), &first); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		encoded, err := toml.New().Marshal(first)
		//: every decoded value can be written back.
		if err != nil {
			t.Errorf("%s: Marshal: %v %v", name, err, errs.FieldsOf(err))
			continue
		}
		var second map[string]any
		//: and what is written decodes.
		if err := toml.New().Unmarshal(encoded, &second); err != nil {
			t.Errorf("%s: re-decode: %v %v\n%s", name, err, errs.FieldsOf(err), encoded)
			continue
		}
		//: to the same value.
		if !sameTagged(tagged(first), tagged(second)) {
			t.Errorf("%s: round trip differs\n%s", name, encoded)
		}
		again, err := toml.New().Marshal(second)
		//: and encodes to the same bytes: the output is deterministic.
		if err != nil || !bytes.Equal(encoded, again) {
			t.Errorf("%s: a second encode differs\n%s\n---\n%s", name, encoded, again)
		}
	}
	//: the suite really ran.
	if ran < 200 {
		t.Fatalf("only %d documents ran", ran)
	}
}
