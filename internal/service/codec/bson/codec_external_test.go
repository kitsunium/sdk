package bson_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/bson"
)

// doc is a top-level BSON document fixture (BSON cannot encode a bare scalar).
type doc struct {
	Name string `bson:"name"`
	Age  int    `bson:"age"`
}

// TestRoundTrip encodes a document and decodes it back without loss.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	c := bson.New()
	data, err := c.Marshal(doc{Name: "kitsunium", Age: 42})
	//: encode must succeed for a struct top level.
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got doc
	//: decode back into a fresh value.
	if uerr := c.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	//: fields must survive the round trip.
	if got.Name != "kitsunium" || got.Age != 42 {
		t.Errorf("round-trip mismatch got=%+v", got)
	}
}

// TestRegisteredViaImport verifies the codec self-registers under name/MIME/ext.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	//: name lookup.
	if _, ok := codec.Lookup("bson"); !ok {
		t.Error("Format \"bson\" not registered")
	}
	//: MIME lookup.
	if _, ok := codec.LookupMIME("application/bson"); !ok {
		t.Error("MIME application/bson not registered")
	}
	//: extension lookup.
	if _, ok := codec.LookupExt(".bson"); !ok {
		t.Error("extension .bson not registered")
	}
}

// TestMarshalRejectsScalar confirms a top-level scalar surfaces a wrapped
// BSON marshal error rather than a panic or silent success.
func TestMarshalRejectsScalar(t *testing.T) {
	t.Parallel()
	//: BSON has no representation for a bare top-level int.
	_, err := bson.New().Marshal(42)
	//: the error must surface (not nil).
	if err == nil {
		t.Fatal("Marshal(scalar) returned nil error, want BSON_MARSHAL_FAILED")
	}
}

// TestUnmarshalSizeCap surfaces BSON_SIZE_EXCEEDED above the 10 MiB cap.
func TestUnmarshalSizeCap(t *testing.T) {
	t.Parallel()
	//: an 11 MiB buffer trips the cap before the decoder runs.
	big := make([]byte, (10<<20)+1)
	var got doc
	err := bson.New().Unmarshal(big, &got)
	//: the cap error must surface and name the size reason.
	if err == nil || !strings.Contains(err.Error(), "BSON_SIZE_EXCEEDED") {
		t.Fatalf("Unmarshal(oversized) err=%v, want BSON_SIZE_EXCEEDED", err)
	}
}
