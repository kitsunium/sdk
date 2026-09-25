package strictjson_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/strictjson"
)

// secret is a value no refusal may ever repeat.
const secret string = "s3cr3t-7f3a9c"

// item is the target most cases decode into.
type item struct {
	Name  string   `json:"name"`
	Price int32    `json:"price"`
	Tags  []string `json:"tags,omitempty"`
}

// order nests items, so a refusal inside an array has a pointer to name.
type order struct {
	Items []item `json:"items"`
}

// TestDecode pins every refusal the package promises, each by its code, and
// the two documents that must still decode.
func TestDecode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		document string
		maxBytes int64
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a well-formed document", document: `{"name":"lamp","price":12}`, maxBytes: 64},
		{name: "exactly the bound", document: `{"name":"lamp","price":12}`, maxBytes: 26},
		{name: "one byte past the bound", document: `{"name":"lamp","price":12} `, maxBytes: 26, wantCode: strictjson.CodeDocumentTooLarge},
		{name: "far past the bound", document: `{"name":"` + strings.Repeat("x", 10_000) + `"}`, maxBytes: 64, wantCode: strictjson.CodeDocumentTooLarge},
		{name: "an empty document", document: "", maxBytes: 64, wantCode: strictjson.CodeDocumentEmpty},
		{name: "whitespace is not a document", document: "   ", maxBytes: 64, wantCode: strictjson.CodeDocumentMalformed},
		{name: "a truncated document", document: `{"name":"lamp"`, maxBytes: 64, wantCode: strictjson.CodeDocumentMalformed},
		{name: "trailing data", document: `{"name":"lamp"} {}`, maxBytes: 64, wantCode: strictjson.CodeDocumentMalformed},
		{name: "a duplicated member", document: `{"name":"lamp","name":"desk"}`, maxBytes: 64, wantCode: strictjson.CodeDocumentMalformed},
		{name: "invalid UTF-8", document: "{\"name\":\"\xff\"}", maxBytes: 64, wantCode: strictjson.CodeDocumentMalformed},
		{name: "an unknown member", document: `{"name":"lamp","admin":true}`, maxBytes: 64, wantCode: strictjson.CodeMemberUnknown},
		{name: "a name differing only by case", document: `{"Name":"lamp"}`, maxBytes: 64, wantCode: strictjson.CodeMemberUnknown},
		{name: "the wrong kind", document: `{"price":"ten"}`, maxBytes: 64, wantCode: strictjson.CodeValueMismatched},
		{name: "a number out of the field's range", document: `{"price":3000000000}`, maxBytes: 64, wantCode: strictjson.CodeValueMismatched},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got item
		err := strictjson.Decode(strings.NewReader(c.document), &got, c.maxBytes)
		if c.wantCode == 0 {
			if err != nil || got.Name != "lamp" || got.Price != 12 {
				t.Fatalf("Decode() = %v, %+v; want the document decoded", err, got)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Decode() = %v, want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecodeRefusesACallNoInputCanSatisfy pins ADR 0031 at the call site: a
// non-positive bound has two opposite readings, and a target that is not a
// non-nil pointer can hold nothing. Both are refused before a byte is read.
func TestDecodeRefusesACallNoInputCanSatisfy(t *testing.T) {
	t.Parallel()
	var target item
	var nilTarget *item
	calls := map[string]func(io.Reader) error{
		"a zero bound":         func(r io.Reader) error { return strictjson.Decode(r, &target, 0) },
		"a negative bound":     func(r io.Reader) error { return strictjson.Decode(r, &target, -1) },
		"a target by value":    func(r io.Reader) error { return strictjson.Decode(r, target, 64) },
		"a nil pointer target": func(r io.Reader) error { return strictjson.Decode(r, nilTarget, 64) },
		"no target at all":     func(r io.Reader) error { return strictjson.Decode(r, nil, 64) },
	}
	if err := strictjson.Decode(nil, &target, 64); !errs.HasCode(err, strictjson.CodeDecodeMisconfigured) {
		t.Errorf("Decode(nil reader) = %v, want DECODE_MISCONFIGURED", err)
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := &countingReader{source: strings.NewReader(`{"name":"lamp"}`)}
			if err := call(source); !errs.HasCode(err, strictjson.CodeDecodeMisconfigured) {
				t.Fatalf("Decode() = %v, want DECODE_MISCONFIGURED", err)
			}
			if source.read != 0 {
				t.Errorf("a refused call read %d bytes", source.read)
			}
		})
	}
}

// countingReader counts the bytes its source handed out.
type countingReader struct {
	source io.Reader
	read   int64
}

// Read counts what it forwards.
func (c *countingReader) Read(buffer []byte) (int, error) {
	count, err := c.source.Read(buffer)
	c.read += int64(count)
	return count, err
}

// endless is a reader that never ends, as a client that keeps sending does.
type endless struct{}

// Read fills the buffer with spaces, forever.
func (endless) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = ' '
	}
	return len(buffer), nil
}

// TestDecodeReadsNoFurtherThanTheBound pins the bound as a bound on READING,
// not a check after the fact: a source that never ends is read for the bound
// plus the one byte that proves it was exceeded, and not a byte more.
func TestDecodeReadsNoFurtherThanTheBound(t *testing.T) {
	t.Parallel()
	source := &countingReader{source: io.MultiReader(strings.NewReader(`{"name":"lamp"}`), endless{})}
	var got item
	err := strictjson.Decode(source, &got, 1000)
	if !errs.HasCode(err, strictjson.CodeDocumentTooLarge) {
		t.Fatalf("Decode() = %v, want DOCUMENT_TOO_LARGE", err)
	}
	if source.read > 1001 {
		t.Errorf("read %d bytes from the source for a bound of 1000", source.read)
	}
}

// TestRefusalsNeverQuoteTheDocument is the package's security property, and
// it is asserted over every rendering a refusal has — Error, Public, Private,
// every field, and the text of everything the chain unwraps to — for a
// recognisable value planted in each position a refusal is about.
func TestRefusalsNeverQuoteTheDocument(t *testing.T) {
	t.Parallel()
	documents := map[string]string{
		"a value of the wrong kind":        `{"price":"` + secret + `"}`,
		"a value beside an unknown member": `{"name":"` + secret + `","admin":true}`,
		"a value before a syntax error":    `{"name":"` + secret + `",}`,
		"a duplicated member's value":      `{"name":"` + secret + `","name":"x"}`,
		"a value past the bound":           `{"name":"` + secret + strings.Repeat("x", 100) + `"}`,
		"a value before trailing data":     `{"name":"` + secret + `"} "` + secret + `"`,
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got item
			err := strictjson.Decode(strings.NewReader(document), &got, 64)
			if err == nil {
				t.Fatal("Decode() = nil, want a refusal")
			}
			for _, text := range renderings(err) {
				if strings.Contains(text, secret) {
					t.Fatalf("a refusal repeated the document: %q", text)
				}
			}
		})
	}
}

// renderings returns every text a refusal can be read as.
func renderings(err error) []string {
	texts := []string{errs.PublicOf(err), errs.PrivateOf(err)}
	for _, field := range errs.FieldsOf(err) {
		texts = append(texts, field.StringValue())
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		texts = append(texts, cause.Error())
	}
	return texts
}

// TestPointerOf pins where a refusal says the document failed: the member or
// element the decoder was on, as a JSON Pointer from the document's own
// names and indices — and the pointer is the ONE place a member name the
// document chose may appear.
func TestPointerOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		document    string
		wantPointer string
	}
	tests := []tc{
		{"the element of an array", `{"items":[{"price":1},{"price":"ten"}]}`, "/items/1/price"},
		{"an unknown member", `{"items":[],"admin":true}`, "/admin"},
		{"a syntax error inside an array", `{"items":[{"price":1},]}`, "/items"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got order
		err := strictjson.Decode(strings.NewReader(c.document), &got, 1024)
		pointer, located := strictjson.PointerOf(err)
		if !located || pointer != c.wantPointer {
			t.Errorf("PointerOf(%v) = %q, %v; want %q", err, pointer, located, c.wantPointer)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	if _, located := strictjson.PointerOf(errors.New("unrelated")); located {
		t.Error("PointerOf located an error this package did not produce")
	}
}

// TestPointerOfIsBounded pins that a document choosing enormous member names
// cannot make a refusal carry them: the pointer is cut back to the deepest
// ancestor that fits in MaxPointerBytes, which still names a real location.
func TestPointerOfIsBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("n", 100)
	document := `{"` + long + `":{"` + long + `":{"` + long + `":1}}}`
	var got map[string]map[string]map[string]string
	err := strictjson.Decode(strings.NewReader(document), &got, 4096)
	pointer, located := strictjson.PointerOf(err)
	if !located || len(pointer) > strictjson.MaxPointerBytes {
		t.Fatalf("PointerOf() = %d bytes, %v; want at most %d", len(pointer), located, strictjson.MaxPointerBytes)
	}
	if pointer != "/"+long+"/"+long {
		t.Errorf("PointerOf() = %q, want the deepest ancestor that fits", pointer)
	}
}

// TestRefusalsCarryTheirStatus pins the HTTP status of each refusal, which is
// what a server answers when it hands the error to errs.HTTPStatusOf.
func TestRefusalsCarryTheirStatus(t *testing.T) {
	t.Parallel()
	statuses := map[*errs.Error]int{
		strictjson.DocumentTooLarge:     http.StatusRequestEntityTooLarge,
		strictjson.DocumentEmpty:        http.StatusBadRequest,
		strictjson.DocumentMalformed:    http.StatusBadRequest,
		strictjson.MemberUnknown:        http.StatusBadRequest,
		strictjson.ValueMismatched:      http.StatusBadRequest,
		strictjson.MediaTypeUnsupported: http.StatusUnsupportedMediaType,
		strictjson.DocumentUnreadable:   http.StatusBadRequest,
		strictjson.DecodeMisconfigured:  http.StatusInternalServerError,
	}
	for sentinel, want := range statuses {
		if got := errs.HTTPStatusOf(sentinel); got != want {
			t.Errorf("%s answers %d, want %d", errs.PublicOf(sentinel), got, want)
		}
	}
}
