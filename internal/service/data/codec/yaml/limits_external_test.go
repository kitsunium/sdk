package yaml_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// The bounds the codec documents, restated: a test that drifts from the real
// bound fails instead of passing on a different one.
const (
	// documentedDepth is the deepest nesting a document may have.
	documentedDepth int = 100
	// documentedNodes is the most nodes a document may hold.
	documentedNodes int = 1 << 20
)

// nestedFlow returns depth nested flow sequences around a scalar.
func nestedFlow(depth int) string {
	//: brackets in, brackets out.
	return strings.Repeat("[", depth) + "x" + strings.Repeat("]", depth)
}

// nestedBlock returns depth nested block mappings.
func nestedBlock(depth int) string {
	var b strings.Builder
	//: one key per level, one step deeper each.
	for i := range depth {
		b.WriteString(strings.Repeat("  ", i) + "k:\n")
	}
	b.WriteString(strings.Repeat("  ", depth) + "leaf\n")
	return b.String()
}

// TestDocumentBounds decodes documents at and past each bound.
func TestDocumentBounds(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		doc    string
		refuse bool
	}
	tests := []tc{
		{name: "flow at the depth bound", doc: nestedFlow(documentedDepth)},
		{name: "flow past the depth bound", doc: nestedFlow(documentedDepth + 1), refuse: true},
		{name: "block at the depth bound", doc: nestedBlock(documentedDepth)},
		{name: "block past the depth bound", doc: nestedBlock(documentedDepth + 1), refuse: true},
		{name: "nodes at the bound", doc: "[" + strings.Repeat("0,", documentedNodes-2) + "0]"},
		{name: "nodes past the bound", doc: "[" + strings.Repeat("0,", documentedNodes-1) + "0]", refuse: true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out any
		err := yaml.New().Unmarshal([]byte(tc.doc), &out)
		if tc.refuse && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: Unmarshal error = %v, want UNMARSHAL_FAILED", tc.name, err)
		}
		if !tc.refuse && err != nil {
			t.Errorf("%s: Unmarshal error = %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncodingIsBounded writes values nested at and past the depth bound.
func TestEncodingIsBounded(t *testing.T) {
	t.Parallel()
	nest := func(depth int) any {
		var v any = "leaf"
		for range depth {
			v = []any{v}
		}
		return v
	}
	type tc struct {
		value  any
		name   string
		refuse bool
	}
	tests := []tc{
		{name: "within the bound", value: nest(documentedDepth - 1)},
		{name: "past the bound", value: nest(documentedDepth + 1), refuse: true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := yaml.New().Marshal(tc.value)
		if tc.refuse && !errs.HasReason(err, "MARSHAL_FAILED") {
			t.Errorf("%s: Marshal error = %v, want MARSHAL_FAILED", tc.name, err)
		}
		if !tc.refuse && err != nil {
			t.Errorf("%s: Marshal error = %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
