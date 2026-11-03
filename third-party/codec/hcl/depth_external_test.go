package hcl_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/codec/hcl"
)

// depthCeiling is the native YAML reader's nesting ceiling, which hcl shares
// (#1099).
const depthCeiling = 100

// anyAttr decodes any attribute of a body.
type anyAttr struct {
	// Rest keeps every attribute, unevaluated.
	Rest map[string]any `hcl:",remain"`
}

// TestUnmarshalRefusesNestingPastTheCeiling is #1099: hclsyntax's
// recursive-descent parser has no depth guard, so `a = ` and a million '['
// overflowed the goroutine stack — a fatal error no recover catches — under
// the 10 MiB byte cap. A pre-scan now refuses past the ceiling with
// DEPTH_EXCEEDED before hclsyntax reads a byte; this test process surviving
// the first row is half the proof.
func TestUnmarshalRefusesNestingPastTheCeiling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		refused bool
	}{
		{"1 MiB of '['", "a = " + strings.Repeat("[", 1<<20), true},
		{"1 MiB of '('", "a = " + strings.Repeat("(", 1<<20), true},
		{"1 MiB of '{'", "a = " + strings.Repeat("{", 1<<20), true},
		{"nested interpolations", "a = " + strings.Repeat(`"${`, 1<<16), true},
		{"a run of unary operators", "a = " + strings.Repeat("!", 1<<20) + "true", true},
		{"a chain of conditionals", "a = " + strings.Repeat("true ? 1 : ", 1<<16) + "2", true},
		{"nested template directives", `a = "` + strings.Repeat("%{ if true }", 1<<14) + `"`, true},
		{"tuples nested at the ceiling", "a = " + strings.Repeat("[", depthCeiling) + "1" + strings.Repeat("]", depthCeiling), false},
		{"tuples nested one past it", "a = " + strings.Repeat("[", depthCeiling+1) + "1" + strings.Repeat("]", depthCeiling+1), true},
		{"brackets inside a string do not nest", `a = "` + strings.Repeat("[", 500) + `"`, false},
		{"brackets in a comment do not nest", "# " + strings.Repeat("[", 500) + "\na = 1\n", false},
		{"brackets in a heredoc do not nest", "a = <<EOT\n" + strings.Repeat("[", 500) + "\nEOT\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var v anyAttr
			err := hcl.New().Unmarshal([]byte(tc.doc), &v)
			if tc.refused {
				if !errs.HasReason(err, "DEPTH_EXCEEDED") || !errs.HasCode(err, hcl.CodeHCLDepthExceeded) {
					t.Fatalf("want DEPTH_EXCEEDED, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want decoded, got %v", err)
			}
		})
	}
}
