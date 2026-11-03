package hcl_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/codec/hcl"
)

// fuzzDoc is the struct the fuzz target decodes into: typed attributes the
// round trip can re-encode, and the rest kept unevaluated.
type fuzzDoc struct {
	Name    string `hcl:"name,optional"`
	Replica int    `hcl:"replica,optional"`
}

// badTag carries an hcl tag kind gohcl does not know, which makes
// gohcl.DecodeBody panic whatever the document holds.
type badTag struct {
	Name string `hcl:"name,nonsense"`
}

// hclSeeds are the documents the suite already decodes, the hostile shapes
// #1099 refuses, and a handful of malformed ones.
func hclSeeds() []string {
	return []string{
		"name = \"kit\"\nreplica = 3\n",
		"name = \"${upper(\"kit\")}\"\n",
		"name = <<EOT\nheredoc\nEOT\n",
		"# comment\nname = \"a\" // trailing\n/* block */\n",
		"a = " + strings.Repeat("[", 200),
		"a = " + strings.Repeat("!", 200) + "true",
		"name = \"%{ if true }x%{ endif }\"\n",
		"block \"label\" {\n  inner = 1\n}\n",
		"name = ",
		"= = =",
		"\"\x00\"",
		"name = \"\\u{110000}\"",
	}
}

// FuzzUnmarshalNeverPanics is #1159: Unmarshal hands untrusted bytes to
// hclsimple.Decode, and a parser panic on a crafted document would take the
// caller's process down. Any input yields nil or a typed refusal — unmarshal
// failure, size or depth — never a panic; a document that decodes re-encodes
// and decodes back equal.
func FuzzUnmarshalNeverPanics(f *testing.F) {
	for _, seed := range hclSeeds() {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var got fuzzDoc
		err := hcl.New().Unmarshal(data, &got)
		//: every refusal is one of the module's three decode codes.
		if err != nil {
			if !errs.HasCode(err, hcl.CodeHCLUnmarshalFailed) && !errs.HasCode(err, hcl.CodeHCLSizeExceeded) && !errs.HasCode(err, hcl.CodeHCLDepthExceeded) {
				t.Fatalf("untyped refusal: %v", err)
			}
			return
		}
		out, merr := hcl.New().Marshal(got)
		if merr != nil {
			t.Fatalf("re-Marshal of a decoded document: %v", merr)
		}
		var back fuzzDoc
		if uerr := hcl.New().Unmarshal(out, &back); uerr != nil || back != got {
			t.Fatalf("round trip: %+v became %+v (%v)", got, back, uerr)
		}
	})
}

// TestUnmarshalRecoversADecodePanic is #1159's unit gate: gohcl.DecodeBody
// panics on a target whose hcl tag it cannot read, and Unmarshal used to let
// that panic reach the caller where Marshal recovered gohcl's. It is now an
// HCL_UNMARSHAL_FAILED, whatever the document.
func TestUnmarshalRecoversADecodePanic(t *testing.T) {
	t.Parallel()
	var target badTag
	err := hcl.New().Unmarshal([]byte("name = \"kit\"\n"), &target)
	if !errs.HasCode(err, hcl.CodeHCLUnmarshalFailed) {
		t.Fatalf("want HCL_UNMARSHAL_FAILED, got %v", err)
	}
}
