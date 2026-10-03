package yaml_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// streaming returns the codec as a codec.StreamingCodec.
func streaming(t *testing.T) codec.StreamingCodec {
	t.Helper()
	sc, ok := yaml.New().(codec.StreamingCodec)
	//: the YAML codec streams.
	if !ok {
		t.Fatal("the YAML codec does not implement codec.StreamingCodec")
	}
	return sc
}

// readAll decodes every document of stream into untyped values.
func readAll(t *testing.T, stream string) ([]any, error) {
	t.Helper()
	dec := streaming(t).NewDecoder(strings.NewReader(stream))
	var docs []any
	//: until the end, or a refusal.
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return docs, err
		}
		docs = append(docs, doc)
	}
}

// TestAStreamIsReadOneDocumentPerDecode reads streams of documents split
// every way YAML splits them.
func TestAStreamIsReadOneDocumentPerDecode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		stream string
		want   []any
	}
	tests := []tc{
		{name: "separated by ---", stream: "a: 1\n---\na: 2\n---\na: 3\n", want: []any{map[string]any{"a": 1}, map[string]any{"a": 2}, map[string]any{"a": 3}}},
		{name: "opened by ---", stream: "---\na: 1\n---\nb: 2\n", want: []any{map[string]any{"a": 1}, map[string]any{"b": 2}}},
		{name: "closed by ...", stream: "a: 1\n...\nb: 2\n...\n", want: []any{map[string]any{"a": 1}, map[string]any{"b": 2}}},
		{name: "comments between", stream: "# head\na: 1\n# tail\n---\n# head\nb: 2\n", want: []any{map[string]any{"a": 1}, map[string]any{"b": 2}}},
		{name: "explicit empty documents", stream: "---\n---\na: 1\n", want: []any{nil, map[string]any{"a": 1}}},
		{name: "block scalar ended by ---", stream: "t: |\n  x\n---\nt: y\n", want: []any{map[string]any{"t": "x\n"}, map[string]any{"t": "y"}}},
		{name: "no trailing line break", stream: "a: 1\n---\nb: 2", want: []any{map[string]any{"a": 1}, map[string]any{"b": 2}}},
		{name: "empty stream", stream: "", want: nil},
		{name: "comments only", stream: "# nothing\n", want: nil},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := readAll(t, tc.stream)
		if err != nil {
			t.Fatalf("%s: Decode error = %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: documents = %#v, want %#v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAStreamRefusalIsLocatedInTheStream checks a refusal inside a later
// document carries its line in the stream, not in the document.
func TestAStreamRefusalIsLocatedInTheStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		stream string
		line   string
		code   errs.Code
	}
	tests := []tc{
		{name: "anchor in the third document", stream: "a: 1\n---\nb: 2\n---\nc: &x 3\n", line: "5", code: coreyaml.CodeYAMLAnchorRefused},
		{name: "syntax in the second document", stream: "a: 1\n---\n\nb: [\n", line: "4", code: coreyaml.CodeYAMLUnmarshalFailed},
		{name: "a directive", stream: "a: 1\n...\n%YAML 1.2\n---\nb: 2\n", line: "3", code: coreyaml.CodeYAMLDirectiveRefused},
		{name: "a ... closing nothing", stream: "a: 1\n...\n...\n", line: "3", code: coreyaml.CodeYAMLUnmarshalFailed},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := readAll(t, tc.stream)
		if code, _ := errs.CodeOf(err); code != tc.code {
			t.Fatalf("%s: error = %v, want code %s", tc.name, err, tc.code)
		}
		if line, _ := position(err); line != tc.line {
			t.Errorf("%s: refused at line %s, want %s", tc.name, line, tc.line)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestTheEncoderWritesADocumentPerEncode writes three values and reads them
// back; a value the encoder refuses does not end the stream.
func TestTheEncoderWritesADocumentPerEncode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"three documents", "a: 1\n---\na: 2\n---\na: 3\n"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		enc := streaming(t).NewEncoder(&buf)
		for i := 1; i <= 3; i++ {
			if err := enc.Encode(map[string]int{"a": i}); err != nil {
				t.Fatalf("%s: Encode(%d) error = %v", tc.name, i, err)
			}
			//: a refused value writes nothing and breaks nothing.
			if err := enc.Encode(make(chan int)); !errs.HasReason(err, "MARSHAL_FAILED") {
				t.Fatalf("%s: Encode(chan) error = %v, want MARSHAL_FAILED", tc.name, err)
			}
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close error = %v", tc.name, err)
		}
		if buf.String() != tc.want {
			t.Errorf("%s: stream = %q, want %q", tc.name, buf.String(), tc.want)
		}
		if err := enc.Encode(1); !errs.HasReason(err, "MARSHAL_FAILED") {
			t.Errorf("%s: Encode after Close error = %v, want MARSHAL_FAILED", tc.name, err)
		}
		docs, err := readAll(t, buf.String())
		if err != nil || len(docs) != 3 {
			t.Errorf("%s: read back %d documents, error %v", tc.name, len(docs), err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// failingReader fails after delivering its content.
type failingReader struct {
	// content is delivered first.
	content *strings.Reader
}

// Read delivers the content, then fails.
func (f failingReader) Read(p []byte) (int, error) {
	if f.content.Len() > 0 {
		return f.content.Read(p)
	}
	return 0, errReaderFailed
}

// errReaderFailed is the failing reader's own error.
var errReaderFailed = errors.New("connection reset")

// endlessReader delivers the same byte forever.
type endlessReader struct{}

// Read fills p.
func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestTheDecoderReportsItsReaderAndItsBound checks a failing reader is an
// UnmarshalFailed with the reader's error kept underneath, and that an
// endless document is refused at the bound instead of read forever.
func TestTheDecoderReportsItsReaderAndItsBound(t *testing.T) {
	t.Parallel()
	type tc struct {
		reader io.Reader
		name   string
		cause  error
	}
	tests := []tc{
		{name: "failing reader", reader: failingReader{content: strings.NewReader("a: 1\n")}, cause: errReaderFailed},
		{name: "endless document", reader: endlessReader{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := streaming(t).NewDecoder(tc.reader)
		var doc any
		err := dec.Decode(&doc)
		if !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("%s: Decode error = %v, want UNMARSHAL_FAILED", tc.name, err)
		}
		if tc.cause != nil && !errors.Is(err, tc.cause) {
			t.Errorf("%s: error %v does not keep the reader's error", tc.name, err)
		}
		if dec.More() {
			t.Errorf("%s: More() = true after a failure", tc.name)
		}
		if err := dec.Decode(&doc); !errors.Is(err, io.EOF) {
			t.Errorf("%s: Decode after a failure = %v, want io.EOF", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
