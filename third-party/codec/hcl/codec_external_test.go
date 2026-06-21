package hcl_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/third-party/codec/hcl"
)

// cfg is a top-level HCL document fixture (gohcl needs a tagged struct).
type cfg struct {
	Name    string `hcl:"name"`
	Replica int    `hcl:"replica"`
}

// TestRoundTrip encodes a struct to HCL and decodes it back without loss.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	c := hcl.New()
	data, err := c.Marshal(cfg{Name: "kitsunium", Replica: 3})
	//: encode must succeed for a tagged struct.
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got cfg
	//: decode back into a fresh value.
	if uerr := c.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("Unmarshal: %v (data=%q)", uerr, data)
	}
	//: fields must survive the round trip.
	if got.Name != "kitsunium" || got.Replica != 3 {
		t.Errorf("round-trip mismatch got=%+v", got)
	}
}

// TestRegisteredViaImport verifies the codec self-registers under name/MIME/ext.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	//: name lookup.
	if _, ok := codec.Lookup("hcl"); !ok {
		t.Error("Format \"hcl\" not registered")
	}
	//: MIME lookup.
	if _, ok := codec.LookupMIME("application/hcl"); !ok {
		t.Error("MIME application/hcl not registered")
	}
	//: extension lookup.
	if _, ok := codec.LookupExt(".hcl"); !ok {
		t.Error("extension .hcl not registered")
	}
}

// TestMarshalRejectsScalar confirms a non-struct surfaces a wrapped error.
func TestMarshalRejectsScalar(t *testing.T) {
	t.Parallel()
	//: HCL marshal is struct-only.
	_, err := hcl.New().Marshal(42)
	//: the error must surface and name the marshal reason.
	if err == nil || !strings.Contains(err.Error(), "HCL_MARSHAL_FAILED") {
		t.Fatalf("Marshal(scalar) err=%v, want HCL_MARSHAL_FAILED", err)
	}
}

// TestUnmarshalSizeCap surfaces HCL_SIZE_EXCEEDED above the 10 MiB cap.
func TestUnmarshalSizeCap(t *testing.T) {
	t.Parallel()
	//: an 11 MiB buffer trips the cap before the parser runs.
	big := make([]byte, (10<<20)+1)
	var got cfg
	err := hcl.New().Unmarshal(big, &got)
	//: the cap error must surface.
	if err == nil || !strings.Contains(err.Error(), "HCL_SIZE_EXCEEDED") {
		t.Fatalf("Unmarshal(oversized) err=%v, want HCL_SIZE_EXCEEDED", err)
	}
}
