package ndjson_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/ndjson"
)

type payload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "ndjson"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := ndjson.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal covers record emission plus VALUE_INVALID / MARSHAL_FAILED.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"slice of records", []payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}}, ""},
		{"non-slice input", 42, "VALUE_INVALID"},
		{"slice of unsupported elements", []chan int{make(chan int)}, "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := ndjson.New().Marshal(tc.in)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Marshal err=%v", tc.name, err)
			} else if !strings.HasSuffix(string(data), "\n") {
				t.Errorf("%s: output missing trailing newline", tc.name)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal covers the success path plus the VALUE_INVALID (non-slice
// target, nil pointer) and UNMARSHAL_FAILED (bad line) branches.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	//: targetKind lets each subtest allocate its own fresh destination
	//: so parallel runs don't race on a shared buffer.
	type targetKind int
	const (
		kindSlice targetKind = iota
		kindString
		kindNilSlice
	)
	type tc struct {
		name    string
		data    []byte
		kind    targetKind
		wantErr string
	}
	tests := []tc{
		{"round-trip success", []byte("{\"name\":\"a\",\"age\":1}\n"), kindSlice, ""},
		{"blank lines are skipped", []byte("{\"name\":\"a\",\"age\":1}\n\n{\"name\":\"b\",\"age\":2}\n"), kindSlice, ""},
		{"non-slice pointer target", []byte("{}\n"), kindString, "VALUE_INVALID"},
		{"nil pointer target", []byte("{}\n"), kindNilSlice, "VALUE_INVALID"},
		{"bad JSON line surfaces UNMARSHAL_FAILED", []byte("not json\n"), kindSlice, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: allocate a per-subtest target so parallel runs don't race.
		var target any
		switch tc.kind {
		case kindSlice:
			target = &[]payload{}
		case kindString:
			s := ""
			target = &s
		case kindNilSlice:
			var nilSlice *[]payload
			target = nilSlice
		}
		err := ndjson.New().Unmarshal(tc.data, target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("ndjson")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("application/x-ndjson"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".ndjson"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
