package yaml_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/codec/yaml"
)

// depthCeiling is gopkg.in/yaml.v3's own nesting bound (max_flow_level and
// max_indents, scannerc.go), which yaml-full now reports as DEPTH_EXCEEDED.
const depthCeiling = 10_000

// nestedFlow returns n nested flow sequences around a scalar.
func nestedFlow(n int) string {
	return strings.Repeat("[", n) + "1" + strings.Repeat("]", n)
}

// TestUnmarshalRefusesNestingPastTheCeiling is #1099. gopkg.in/yaml.v3
// v3.0.1 does bound nesting — at 10 000 flow levels and 10 000 indentation
// levels — so a megabyte of '[' was refused, not a crash; but the refusal was
// a YAML_FULL_UNMARSHAL_FAILED indistinguishable from a syntax error. It is
// now DEPTH_EXCEEDED, the reason every native reader's nesting refusal
// carries, with its own code — read from the document's structure, so a
// syntax error in a deep but legal document keeps its own reason.
func TestUnmarshalRefusesNestingPastTheCeiling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		refused bool
	}{
		{"1 MiB of '['", strings.Repeat("[", 1<<20), true},
		{"1 MiB of '- '", strings.Repeat("- ", 1<<19) + "x\n", true},
		{"1 MiB of '{a: '", strings.Repeat("{a: ", 1<<18), true},
		{"flow nesting at the ceiling", nestedFlow(depthCeiling), false},
		{"flow nesting one past it", nestedFlow(depthCeiling + 1), true},
		{"a syntax error is not a depth refusal", "k: [1, 2", false},
		{"brackets in quotes and comments do not count", "k: '" + strings.Repeat("[", 20_000) + "' # " + strings.Repeat("{", 20_000) + "\n: :", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var v any
			err := yaml.New().Unmarshal([]byte(tc.doc), &v)
			if tc.refused {
				if !errs.HasReason(err, "DEPTH_EXCEEDED") || !errs.HasCode(err, yaml.CodeYAMLFullDepthExceeded) {
					t.Fatalf("want DEPTH_EXCEEDED, got %v", err)
				}
				return
			}
			if errs.HasReason(err, "DEPTH_EXCEEDED") {
				t.Fatalf("want no depth refusal, got %v", err)
			}
		})
	}
}
