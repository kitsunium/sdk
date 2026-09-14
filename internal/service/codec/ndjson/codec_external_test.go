package ndjson_test

import (
	stdjson "encoding/json"
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

// ptrMarshaler carries MarshalJSON on a POINTER receiver, so it is only
// reachable when the encoder is handed an addressable value.
type ptrMarshaler struct {
	V int `json:"v"`
}

// MarshalJSON emits a sentinel that cannot be produced by struct reflection,
// so its absence from the output is unambiguous.
func (p *ptrMarshaler) MarshalJSON() ([]byte, error) {
	//: sentinel distinguishable from the reflected form {"v":N}.
	return []byte(`"PTR-JSON"`), nil
}

// ptrText carries MarshalText on a POINTER receiver — the same reachability
// question one interface down.
type ptrText struct {
	V int `json:"v"`
}

// MarshalText emits a sentinel for the same reason as ptrMarshaler.
func (p *ptrText) MarshalText() ([]byte, error) {
	//: encoding/json quotes a MarshalText result, so this lands as "PTR-TEXT".
	return []byte("PTR-TEXT"), nil
}

// nested exercises a record shape the fixed {name,age} budget batch never
// reaches: embedded pointer, omitempty holes, map, raw message, unicode.
type nested struct {
	ID    string             `json:"id"`
	Tags  []string           `json:"tags,omitempty"`
	Meta  map[string]float64 `json:"meta,omitempty"`
	Inner *payload           `json:"inner,omitempty"`
	Raw   stdjson.RawMessage `json:"raw,omitempty"`
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

// TestStdlibSliceParity pins the contract that makes ndjson's per-record loop
// substitutable for the stdlib: line i of Marshal(v) must be byte-identical to
// element i of stdjson.Marshal(v) — i.e. encoding a []T record-by-record must
// agree with encoding the whole []T at once.
//
// It did NOT hold before elemForMarshal. reflect.Value.Interface() strips
// addressability, so a MarshalJSON/MarshalText declared on *T was skipped:
// ndjson emitted {"v":1} where stdjson.Marshal([]T{...}) emits "PTR-JSON".
// Measured identical on go1.26.8 and go1.27.0, so this was never a toolchain
// behaviour change — it was ndjson's own divergence.
//
// The table deliberately varies record SHAPE, not record count: a probe that
// only scales the same shape is unanimous and blind.
func TestStdlibSliceParity(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
	}
	tests := []tc{
		{"pointer-receiver MarshalJSON", []ptrMarshaler{{V: 1}, {V: 2}}},
		{"pointer-receiver MarshalText", []ptrText{{V: 1}, {V: 2}}},
		{"plain structs", []payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}}},
		{"slice of pointers incl. nil", []*payload{{Name: "a", Age: 1}, nil}},
		{"heterogeneous any", []any{1, "s", nil, true, payload{Name: "a", Age: 1}}},
		{"nested + unicode + holes", []nested{
			{ID: "\u00e9\u4e2d\u6587", Tags: []string{"x"}, Meta: map[string]float64{"k": 1.5}, Inner: &payload{Name: "z", Age: 9}, Raw: stdjson.RawMessage(`{"r":1}`)},
			{ID: "plain"},
		}},
		{"scalars", []int{1, -2, 0}},
		{"strings needing escapes", []string{"a\nb", `"q"`, "\\", "\u2028"}},
		{"byte slices", [][]byte{[]byte("embedded\nnewline")}},
		{"non-addressable array value", [2]payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: reference: the stdlib's own encoding of the whole slice, split back
		//: into its elements. RawMessage keeps each element's exact bytes.
		whole, werr := stdjson.Marshal(tc.in)
		//: a broken reference would make the comparison below vacuous.
		if werr != nil {
			t.Fatalf("%s: reference stdjson.Marshal: %v", tc.name, werr)
		}
		var want []stdjson.RawMessage
		//: RawMessage keeps each element's exact bytes — no re-encoding.
		if uerr := stdjson.Unmarshal(whole, &want); uerr != nil {
			t.Fatalf("%s: reference split: %v", tc.name, uerr)
		}
		got, merr := ndjson.New().Marshal(tc.in)
		//: the subject of the comparison; an error here is not a mismatch.
		if merr != nil {
			t.Fatalf("%s: ndjson Marshal: %v", tc.name, merr)
		}
		lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
		//: a count mismatch makes the per-element loop compare the wrong pairs,
		//: so stop here rather than report a cascade of bogus differences.
		if len(lines) != len(want) {
			t.Fatalf("%s: %d lines, stdlib has %d elements", tc.name, len(lines), len(want))
		}
		//: compare element by element rather than the joined blob, so a
		//: failure names the offending record.
		for i := range want {
			//: the contract: line i IS the stdlib's element i, byte for byte.
			if lines[i] != string(want[i]) {
				t.Errorf("%s: line %d = %s, stdlib element = %s", tc.name, i, lines[i], want[i])
			}
		}
	}
	//: one sub-test per record SHAPE — a divergence names its own shape.
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
