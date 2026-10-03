// Package yaml_test pins the codes internal/core/data/codec/yaml declares: the
// value each was allocated with, which a move of its declaration never
// changes (ADR 0160 §3), and the sentinel bound to it.
package yaml_test

import (
	"testing"

	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestCodesKeepTheirValues pins every code to its literal value and to the
// sentinel that carries it, reason included. A failure here is a wire
// change: a dashboard, an alert rule or a client branches on these numbers.
func TestCodesKeepTheirValues(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel *errs.Error
		code     errs.Code
		want     uint32
		reason   string
	}
	tests := []tc{
		{"MarshalFailed", coreyaml.MarshalFailed, coreyaml.CodeYAMLMarshalFailed, 0x00_03_04_01, "MARSHAL_FAILED"},
		{"UnmarshalFailed", coreyaml.UnmarshalFailed, coreyaml.CodeYAMLUnmarshalFailed, 0x00_03_04_02, "UNMARSHAL_FAILED"},
		{"AnchorRefused", coreyaml.AnchorRefused, coreyaml.CodeYAMLAnchorRefused, 0x00_03_04_03, "ANCHOR_REFUSED"},
		{"AliasRefused", coreyaml.AliasRefused, coreyaml.CodeYAMLAliasRefused, 0x00_03_04_04, "ALIAS_REFUSED"},
		{"TagRefused", coreyaml.TagRefused, coreyaml.CodeYAMLTagRefused, 0x00_03_04_05, "TAG_REFUSED"},
		{"MergeKeyRefused", coreyaml.MergeKeyRefused, coreyaml.CodeYAMLMergeKeyRefused, 0x00_03_04_06, "MERGE_KEY_REFUSED"},
		{"MultipleDocumentsRefused", coreyaml.MultipleDocumentsRefused, coreyaml.CodeYAMLMultiDocRefused, 0x00_03_04_07, "MULTIPLE_DOCUMENTS_REFUSED"},
		{"ComplexKeyRefused", coreyaml.ComplexKeyRefused, coreyaml.CodeYAMLComplexKeyRefused, 0x00_03_04_08, "COMPLEX_KEY_REFUSED"},
		{"DirectiveRefused", coreyaml.DirectiveRefused, coreyaml.CodeYAMLDirectiveRefused, 0x00_03_04_09, "DIRECTIVE_REFUSED"},
		{"DuplicateKey", coreyaml.DuplicateKey, coreyaml.CodeYAMLDuplicateKey, 0x00_03_04_0A, "DUPLICATE_KEY"},
		{"LeadingZeroRefused", coreyaml.LeadingZeroRefused, coreyaml.CodeYAMLLeadingZeroRefused, 0x00_03_04_0B, "LEADING_ZERO_REFUSED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the value allocated with the range, unchanged.
		if uint32(c.code) != c.want {
			t.Errorf("%s: code = %#x, want %#x", c.name, uint32(c.code), c.want)
		}
		//: the sentinel carries that code.
		if got := c.sentinel.Code(); got != c.code {
			t.Errorf("%s: sentinel code = %v, want %v", c.name, got, c.code)
		}
		//: and the reason it has always rendered.
		if got := c.sentinel.Reason(); got != c.reason {
			t.Errorf("%s: sentinel reason = %q, want %q", c.name, got, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
