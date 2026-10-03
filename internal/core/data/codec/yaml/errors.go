// Package yaml — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed marks a value the encoder cannot write as YAML.
	MarshalFailed = errs.Define(CodeYAMLMarshalFailed, "MARSHAL_FAILED",
		"YAML encoding failed",
		"service/data/codec/yaml: the value cannot be written in the supported YAML subset")

	// UnmarshalFailed marks a document the decoder cannot read into its target.
	UnmarshalFailed = errs.Define(CodeYAMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"YAML decoding failed",
		"service/data/codec/yaml: not YAML of the supported subset, past a bound, or a value its target cannot hold")

	// AnchorRefused marks an anchor, refused by name.
	AnchorRefused = errs.Define(CodeYAMLAnchorRefused, "ANCHOR_REFUSED",
		"YAML anchors (&) are outside the supported subset",
		"service/data/codec/yaml: an anchor names a node for an alias to repeat; the subset has neither")

	// AliasRefused marks an alias, refused by name.
	AliasRefused = errs.Define(CodeYAMLAliasRefused, "ALIAS_REFUSED",
		"YAML aliases (*) are outside the supported subset",
		"service/data/codec/yaml: an alias repeats an anchored node; refused, so no document expands past its size")

	// TagRefused marks an explicit tag, refused by name.
	TagRefused = errs.Define(CodeYAMLTagRefused, "TAG_REFUSED",
		"YAML tags (! and !!) are outside the supported subset",
		"service/data/codec/yaml: a tag overrides the core schema; the subset resolves every scalar by the core schema alone")

	// MergeKeyRefused marks a merge key, refused by name.
	MergeKeyRefused = errs.Define(CodeYAMLMergeKeyRefused, "MERGE_KEY_REFUSED",
		"YAML merge keys (<<) are outside the supported subset",
		"service/data/codec/yaml: a merge key is a YAML 1.1 type that copies another mapping; quote it to mean the text <<")

	// MultipleDocumentsRefused marks a second document in a single-document read.
	MultipleDocumentsRefused = errs.Define(CodeYAMLMultiDocRefused, "MULTIPLE_DOCUMENTS_REFUSED",
		"YAML input holds more than one document",
		"service/data/codec/yaml: Unmarshal reads exactly one document; a stream of documents is read with NewDecoder")

	// ComplexKeyRefused marks a complex mapping key, refused by name.
	ComplexKeyRefused = errs.Define(CodeYAMLComplexKeyRefused, "COMPLEX_KEY_REFUSED",
		"YAML complex keys (?) are outside the supported subset",
		"service/data/codec/yaml: a mapping key must be a scalar on one line; ? and collection keys are refused")

	// DirectiveRefused marks a directive, refused by name.
	DirectiveRefused = errs.Define(CodeYAMLDirectiveRefused, "DIRECTIVE_REFUSED",
		"YAML directives (%YAML, %TAG) are outside the supported subset",
		"service/data/codec/yaml: the subset is YAML 1.2.2 with no tag handle, so a directive has nothing to declare")

	// DuplicateKey marks a mapping that holds the same key twice.
	DuplicateKey = errs.Define(CodeYAMLDuplicateKey, "DUPLICATE_KEY",
		"YAML mapping holds the same key twice",
		"service/data/codec/yaml: two keys of one mapping read the same; neither is chosen over the other")

	// LeadingZeroRefused marks an integer whose leading zero changes its value
	// between YAML versions.
	LeadingZeroRefused = errs.Define(CodeYAMLLeadingZeroRefused, "LEADING_ZERO_REFUSED",
		"YAML integer with a leading zero is octal in YAML 1.1 and decimal in 1.2",
		"service/data/codec/yaml: write 0o644 for octal, 644 for decimal, or quote the text")
)
