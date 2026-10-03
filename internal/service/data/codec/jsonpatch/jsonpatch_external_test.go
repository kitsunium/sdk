// Package jsonpatch_test — the diff pinned on the cases a reader checks by
// eye, and proved on thousands it cannot: every patch, applied in order by an
// RFC 6902 applier written here, turns the first document into the second.
package jsonpatch_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/jsonpatch"
)

// edit is an operation written the short way a case expects it.
type edit struct {
	op, path, value, old string
}

// short writes an operation the way the cases spell theirs.
func short(e jsonpatch.EditValue) edit {
	return edit{op: string(e.Op), path: e.Path, value: string(e.Value), old: string(e.Old)}
}

// diffOf runs Diff over two documents written as text, failing the test on
// an error.
func diffOf(t *testing.T, from, to string) []edit {
	t.Helper()
	edits, err := jsonpatch.Diff([]byte(from), []byte(to))
	if err != nil {
		t.Fatalf("Diff(%s, %s) = %v", from, to, err)
	}
	if edits == nil {
		t.Fatalf("Diff(%s, %s) returned nil, want a slice", from, to)
	}
	out := make([]edit, len(edits))
	for i, e := range edits {
		out[i] = short(e)
	}
	return out
}

// TestDiff pins the operations for the cases a reader checks by eye: objects
// nested and flat, members added and removed in name order, arrays edited at
// either end and in the middle, kinds changed, names escaped, and values the
// RFC calls equal however they are written.
func TestDiff(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, from, to string
		want           []edit
	}{
		{"the same document", `{"a":1,"b":[1,2]}`, `{"a":1,"b":[1,2]}`, []edit{}},
		{"members in another order, spaces", `{"a":1,"b":2}`, "{ \"b\" : 2,\n \"a\" : 1 }", []edit{}},
		{"one number written three ways", `[1, 100, 0.5, -0]`, `[1.0, 1e2, 5E-1, 0]`, []edit{}},
		{"every zero is zero", `[0, -0, 0.0, 0e5, -0.000E-7]`, `[0, 0, 0, 0, 0]`, []edit{}},
		{
			"members in another order, nested in an array", `[{"a":{"x":1,"y":[1,{"p":2,"q":3}]},"b":2}]`,
			`[{"b":2,"a":{"y":[1,{"q":3,"p":2}],"x":1}}]`,
			[]edit{},
		},
		{"one string escaped two ways", `"caf\u00e9"`, `"café"`, []edit{}},
		{
			"an exponent no float holds, compared exactly", `[1e999999999999999999999, 2e-999999999999999999999]`,
			`[10E+999999999999999999998, 0.2e-999999999999999999998]`,
			[]edit{},
		},
		{"an exponent no float holds, told apart", `[1e999999999999999999999]`, `[1e999999999999999999998]`, []edit{
			{op: "replace", path: "/0", value: "1e999999999999999999998", old: "1e999999999999999999999"},
		}},
		{"a nested member changed, one added", `{"a":{"b":1,"c":2}}`, `{"a":{"b":1,"c":3,"d":4}}`, []edit{
			{op: "replace", path: "/a/c", value: "3", old: "2"},
			{op: "add", path: "/a/d", value: "4"},
		}},
		{"members removed and added, in name order", `{"z":1,"b":{"x":true},"m":null}`, `{"a":"new","m":null,"y":[]}`, []edit{
			{op: "add", path: "/a", value: `"new"`},
			{op: "remove", path: "/b", old: `{"x":true}`},
			{op: "add", path: "/y", value: `[]`},
			{op: "remove", path: "/z", old: "1"},
		}},
		{"names that need escaping", `{"a/b":1,"m~n":2,"~1":3}`, `{"a/b":2,"m~n":3,"~1":4}`, []edit{
			{op: "replace", path: "/a~1b", value: "2", old: "1"},
			{op: "replace", path: "/m~0n", value: "3", old: "2"},
			{op: "replace", path: "/~01", value: "4", old: "3"},
		}},
		{"elements appended", `[1,2]`, `[1,2,3,4]`, []edit{
			{op: "add", path: "/2", value: "3"},
			{op: "add", path: "/3", value: "4"},
		}},
		{"an element inserted in the middle", `[1,2,3,4]`, `[1,2,9,3,4]`, []edit{{op: "add", path: "/2", value: "9"}}},
		{"an element removed from the middle", `["a","b","c","d"]`, `["a","c","d"]`, []edit{{op: "remove", path: "/1", old: `"b"`}}},
		{"elements removed from the end", `[1,2,3,4]`, `[1,2]`, []edit{
			{op: "remove", path: "/2", old: "3"},
			{op: "remove", path: "/2", old: "4"},
		}},
		{"an element prepended", `[{"id":1},{"id":2}]`, `[{"id":0},{"id":1},{"id":2}]`, []edit{
			{op: "add", path: "/0", value: `{"id":0}`},
		}},
		{
			"an element changed in place, nested", `{"blocks":[{"id":1,"text":"a"},{"id":2,"text":"b"}]}`,
			`{"blocks":[{"id":1,"text":"a"},{"id":2,"text":"B","bold":true}]}`,
			[]edit{
				{op: "add", path: "/blocks/1/bold", value: "true"},
				{op: "replace", path: "/blocks/1/text", value: `"B"`, old: `"b"`},
			},
		},
		{"a block moved is a removal and an insertion", `["a","b","c"]`, `["b","c","a"]`, []edit{
			{op: "remove", path: "/0", old: `"a"`},
			{op: "add", path: "/2", value: `"a"`},
		}},
		{"an array becomes an object", `{"a":[1]}`, `{"a":{"x":1}}`, []edit{{op: "replace", path: "/a", value: `{"x":1}`, old: "[1]"}}},
		{"the root changes kind", `[1]`, `{"a":1}`, []edit{{op: "replace", path: "", value: `{"a":1}`, old: "[1]"}}},
		{"a scalar root", `true`, `null`, []edit{{op: "replace", path: "", value: "null", old: "true"}}},
		{"a number keeps its spelling", `{"n":1}`, `{"n":1.50e+3}`, []edit{{op: "replace", path: "/n", value: "1.50e+3", old: "1"}}},
	} {
		if got := diffOf(t, c.from, c.to); !slices.Equal(got, c.want) {
			t.Errorf("%s: Diff =\n%+v\nwant\n%+v", c.name, got, c.want)
		}
	}
}

// TestAnEditIsAJSONPatchOperation pins the JSON of an operation: RFC 6902's
// op, path and value, and the value replaced or removed under "old", which an
// applier ignores; absent values are absent, and a null value is present.
func TestAnEditIsAJSONPatchOperation(t *testing.T) {
	t.Parallel()
	edits, err := jsonpatch.Diff([]byte(`{"a":1,"b":2}`), []byte(`{"a":null,"c":3}`))
	if err != nil {
		t.Fatalf("Diff() = %v", err)
	}
	raw, err := json.Marshal(edits)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	want := `[{"op":"replace","path":"/a","value":null,"old":1},{"op":"remove","path":"/b","old":2},{"op":"add","path":"/c","value":3}]`
	if string(raw) != want {
		t.Fatalf("the patch is\n%s\nwant\n%s", raw, want)
	}
}

// TestDocumentsThatAreNotJSON pins NotJSON: which document, where, and never
// a byte of it — a duplicated member name, invalid UTF-8 and trailing data
// refused like a syntax error.
func TestDocumentsThatAreNotJSON(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t"
	valid := []byte(`{"ok":true}`)
	for name, doc := range map[string]string{
		"a syntax error":            `{"a":` + secret + `}`,
		"a truncation":              `{"a":"` + secret,
		"trailing data":             `{"a":1} "` + secret + `"`,
		"a duplicated member":       `{"a":"` + secret + `","a":2}`,
		"invalid UTF-8":             "\"\xff" + secret + "\"",
		"nothing at all":            ``,
		"two values with no comma":  `["` + secret + `" "x"]`,
		"a member without a value":  `{"` + secret + `"}`,
		"an unterminated array":     `[1, 2, "` + secret + `"`,
		"a number that is not one":  `01` + secret,
		"a bare word":               secret,
		"a comment":                 `// ` + secret + "\n1",
		"nesting past the reader's": strings.Repeat("[", 20000) + strings.Repeat("]", 20000),
	} {
		for _, which := range []string{"from", "to"} {
			from, to := []byte(doc), valid
			if which == "to" {
				from, to = valid, []byte(doc)
			}
			edits, err := jsonpatch.Diff(from, to)
			if !errs.HasCode(err, jsonpatch.CodeNotJSON) || edits != nil {
				t.Fatalf("%s as %s: Diff = %v, %v, want NotJSON", name, which, edits, err)
			}
			text := err.Error() + errs.PublicOf(err) + errs.PrivateOf(err)
			for _, field := range errs.FieldsOf(err) {
				text += " " + field.StringValue()
			}
			if strings.Contains(text, secret) {
				t.Fatalf("%s as %s: the refusal quotes the document: %s", name, which, text)
			}
			if document := fieldOf(err, "document"); document != which {
				t.Fatalf("%s as %s: the refusal names the document %q", name, which, document)
			}
		}
	}
}

// fieldOf returns the value of the field key of err, or "".
func fieldOf(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// TestEveryPatchTurnsTheFirstDocumentIntoTheSecond pins the property the
// operations exist for, on four thousand pairs of generated documents — the
// second a random rewrite of the first, nested objects and arrays included:
// applied in order, each operation's old value found where it says, the patch
// turns the first document into the second; and a document compared with
// itself gives none.
func TestEveryPatchTurnsTheFirstDocumentIntoTheSecond(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(20260928, 143))
	for round := range 4000 {
		a := randomValue(rng, 0)
		b := mutate(rng, a, 0)
		from, to := mustJSON(t, a), mustJSON(t, b)
		edits, err := jsonpatch.Diff(from, to)
		if err != nil {
			t.Fatalf("round %d: Diff(%s, %s) = %v", round, from, to, err)
		}
		got := applyAll(t, decoded(t, from), edits)
		if !bytes.Equal(mustJSON(t, got), mustJSON(t, decoded(t, to))) {
			t.Fatalf("round %d: the patch\n%+v\nturns %s into %s, want %s", round, edits, from, mustJSON(t, got), to)
		}
		if same, sameErr := jsonpatch.Diff(from, from); sameErr != nil || len(same) != 0 {
			t.Fatalf("round %d: a document against itself = %+v, %v", round, same, sameErr)
		}
	}
}

// TestLongArraysArePairedByPosition pins the bound on alignment. The equal
// elements at both ends are trimmed whatever the length, so a prepended
// element is one add even in a long array; but differing middles too long to
// align are compared element by element — a longer patch, still a correct
// one — where shorter ones are aligned.
func TestLongArraysArePairedByPosition(t *testing.T) {
	t.Parallel()
	numbers := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = json.Number(strconv.Itoa(i))
		}
		return out
	}
	// shiftedAndEdited prepends -1 and changes the last element: nothing is
	// equal at either end, so the whole arrays are the middles.
	shiftedAndEdited := func(a []any) []any {
		return append(append([]any{json.Number("-1")}, a[:len(a)-1]...), json.Number("999"))
	}
	for _, c := range []struct {
		name  string
		from  []any
		to    []any
		edits int
		// applied checks the patch with the suite's applier, which copies an
		// array per operation: too slow for the longest case, whose pairing is
		// the 700-element case's.
		applied bool
	}{
		{"a prepended element, trimmed at the end", numbers(700), append([]any{json.Number("-1")}, numbers(700)...), 1, true},
		{"middles of 300 elements, aligned", numbers(300), shiftedAndEdited(numbers(300)), 2, true},
		{"middles of 700 elements, paired by position", numbers(700), shiftedAndEdited(numbers(700)), 701, true},
		// 50 001 × 50 000 cells overflow a 32-bit int: the bound must hold on
		// 386 too, where a product would wrap negative and pass it.
		{"middles whose product overflows 32 bits, paired by position", numbers(50_000), shiftedAndEdited(numbers(50_000)), 50_001, false},
	} {
		from, to := mustJSON(t, c.from), mustJSON(t, c.to)
		edits, err := jsonpatch.Diff(from, to)
		if err != nil {
			t.Fatalf("%s: Diff() = %v", c.name, err)
		}
		if len(edits) != c.edits {
			t.Fatalf("%s: %d operations, want %d", c.name, len(edits), c.edits)
		}
		if !c.applied {
			continue
		}
		if got := applyAll(t, decoded(t, from), edits); !bytes.Equal(mustJSON(t, got), to) {
			t.Fatalf("%s: the patch does not turn the first array into the second", c.name)
		}
	}
}

// intNer is what generating a document needs of a random source.
type intNer interface {
	// IntN returns a number in [0, n).
	IntN(n int) int
}

// keys are the member names generated documents use, the two RFC 6901
// escapes among them.
var keys = []string{"a", "b", "c", "d", "title", "a/b", "m~n", "~1", ""}

// randomValue generates a JSON value at depth, containers thinning with depth.
func randomValue(rng intNer, depth int) any {
	switch pick := rng.IntN(10); {
	case pick < 3 && depth < 4:
		obj := map[string]any{}
		for range rng.IntN(5) {
			obj[keys[rng.IntN(len(keys))]] = randomValue(rng, depth+1)
		}
		return obj
	case pick < 6 && depth < 4:
		arr := make([]any, rng.IntN(7))
		for i := range arr {
			arr[i] = randomValue(rng, depth+1)
		}
		return arr
	default:
		return randomScalar(rng)
	}
}

// randomScalar generates a string, a number, a boolean or null.
func randomScalar(rng intNer) any {
	switch rng.IntN(5) {
	case 0:
		return fmt.Sprintf("s%d", rng.IntN(4))
	case 1:
		return json.Number(strconv.Itoa(rng.IntN(5)))
	case 2:
		return true
	case 3:
		return false
	default:
		return nil
	}
}

// mutate returns a copy of v with some of its parts rewritten: scalars
// changed, members added and removed, elements inserted, removed and changed,
// kinds changed.
func mutate(rng intNer, v any, depth int) any {
	if rng.IntN(8) == 0 {
		return randomValue(rng, depth)
	}
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, member := range value {
			if rng.IntN(6) == 0 {
				continue
			}
			out[k] = mutate(rng, member, depth+1)
		}
		if rng.IntN(3) == 0 {
			out[keys[rng.IntN(len(keys))]] = randomValue(rng, depth+1)
		}
		return out
	case []any:
		var out []any
		for _, item := range value {
			switch rng.IntN(8) {
			case 0:
			case 1:
				out = append(out, randomValue(rng, depth+1), mutate(rng, item, depth+1))
			default:
				out = append(out, mutate(rng, item, depth+1))
			}
		}
		if rng.IntN(3) == 0 {
			at := rng.IntN(len(out) + 1)
			out = slices.Insert(out, at, randomValue(rng, depth+1))
		}
		if out == nil {
			out = []any{}
		}
		return out
	default:
		if rng.IntN(3) == 0 {
			return randomScalar(rng)
		}
		return value
	}
}

// mustJSON encodes v, map members sorted, numbers as written.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	return raw
}

// decoded decodes raw with numbers kept as written.
func decoded(t *testing.T, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("Decode(%s) = %v", raw, err)
	}
	return v
}

// applyAll applies edits to doc in order, as RFC 6902 applies a patch, and
// checks each one's old value is what the path holds before it applies.
func applyAll(t *testing.T, doc any, edits []jsonpatch.EditValue) any {
	t.Helper()
	for _, e := range edits {
		tokens := pointerTokens(t, e.Path)
		if e.Op != jsonpatch.Add {
			if at := valueAt(t, doc, tokens); !bytes.Equal(mustJSON(t, at), mustJSON(t, decoded(t, e.Old))) {
				t.Fatalf("%s %s: the old value is %s, the document holds %s", e.Op, e.Path, e.Old, mustJSON(t, at))
			}
		}
		var value any
		if e.Op != jsonpatch.Remove {
			value = decoded(t, e.Value)
		}
		doc = applyAt(t, doc, tokens, e.Op, value)
	}
	return doc
}

// pointerTokens splits an RFC 6901 pointer into its unescaped tokens.
func pointerTokens(t *testing.T, pointer string) []string {
	t.Helper()
	if pointer == "" {
		return nil
	}
	if !strings.HasPrefix(pointer, "/") {
		t.Fatalf("the pointer %q does not start with /", pointer)
	}
	tokens := strings.Split(pointer[1:], "/")
	for i, token := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens
}

// valueAt returns the value tokens reach in doc.
func valueAt(t *testing.T, doc any, tokens []string) any {
	t.Helper()
	for _, token := range tokens {
		switch container := doc.(type) {
		case map[string]any:
			member, found := container[token]
			if !found {
				t.Fatalf("no member %q", token)
			}
			doc = member
		case []any:
			doc = container[index(t, token, len(container)-1)]
		default:
			t.Fatalf("the pointer goes through a scalar at %q", token)
		}
	}
	return doc
}

// applyAt applies one operation at tokens in doc and returns the result.
func applyAt(t *testing.T, doc any, tokens []string, op jsonpatch.Op, value any) any {
	t.Helper()
	if len(tokens) == 0 {
		if op != jsonpatch.Replace {
			t.Fatalf("%s at the root", op)
		}
		return value
	}
	token, rest := tokens[0], tokens[1:]
	switch container := doc.(type) {
	case map[string]any:
		out := make(map[string]any, len(container)+1)
		maps.Copy(out, container)
		switch {
		case len(rest) > 0:
			out[token] = applyAt(t, container[token], rest, op, value)
		case op == jsonpatch.Remove:
			if _, found := out[token]; !found {
				t.Fatalf("remove of a missing member %q", token)
			}
			delete(out, token)
		case op == jsonpatch.Replace:
			if _, found := out[token]; !found {
				t.Fatalf("replace of a missing member %q", token)
			}
			out[token] = value
		default:
			out[token] = value
		}
		return out
	case []any:
		out := slices.Clone(container)
		switch {
		case len(rest) > 0:
			i := index(t, token, len(out)-1)
			out[i] = applyAt(t, out[i], rest, op, value)
		case op == jsonpatch.Remove:
			out = slices.Delete(out, index(t, token, len(out)-1), index(t, token, len(out)-1)+1)
		case op == jsonpatch.Replace:
			out[index(t, token, len(out)-1)] = value
		default:
			out = slices.Insert(out, index(t, token, len(out)), value)
		}
		return out
	default:
		t.Fatalf("the pointer goes through a scalar at %q", token)
		return nil
	}
}

// index reads an array index token, at most highest.
func index(t *testing.T, token string, highest int) int {
	t.Helper()
	i, err := strconv.Atoi(token)
	if err != nil || i < 0 || i > highest || (len(token) > 1 && token[0] == '0') {
		t.Fatalf("the index %q is not one of 0 to %d", token, highest)
	}
	return i
}
