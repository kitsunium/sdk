package json_test

import (
	stdjson "encoding/json"
	"strings"
)

// appendRecord is a struct with the members encoding/json treats specially.
type appendRecord struct {
	Name    string             `json:"name"`
	HTML    string             `json:"html"`
	Skip    string             `json:"-"`
	Empty   string             `json:"empty,omitempty"`
	Count   int64              `json:"count,string"`
	Tags    []string           `json:"tags"`
	Attrs   map[string]string  `json:"attrs"`
	Raw     stdjson.RawMessage `json:"raw"`
	Pointer *int               `json:"pointer"`
}

// appendFixtures are the values whose Append must write Marshal's bytes.
func appendFixtures() map[string]any {
	seven := 7
	return map[string]any{
		"struct":       appendRecord{Name: "a", HTML: "<b>&</b>", Count: 42, Tags: []string{"x"}, Attrs: map[string]string{"k": "v", "a": "b"}, Raw: stdjson.RawMessage(`{"z":1}`), Pointer: &seven},
		"zero struct":  appendRecord{},
		"pointer":      &appendRecord{Name: "p"},
		"map":          map[string]any{"b": 1, "a": []any{true, nil, 1.5, " "}},
		"nil":          nil,
		"string":       "invalid \xff utf-8",
		"raw message":  stdjson.RawMessage(`[1,2]`),
		"raw pointer":  &stdjson.RawMessage{'{', '}'},
		"empty raw":    stdjson.RawMessage(nil),
		"bytes":        []byte("base64"),
		"one kibibyte": kibValue(),
	}
}

// appendFailure is a value Marshal refuses after it has written more than an
// encoder's buffer: a large member, then a channel.
func appendFailure() any {
	return struct {
		Big string
		Bad chan int
	}{Big: strings.Repeat("x", 64<<10), Bad: make(chan int)}
}

// kibValue is a struct of about 1 KiB of JSON.
func kibValue() *appendRecord {
	tags := make([]string, 0, 60)
	for range 60 {
		tags = append(tags, "tag-value")
	}
	return &appendRecord{Name: strings.Repeat("n", 200), HTML: "<p>", Count: 9, Tags: tags}
}
