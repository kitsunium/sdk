// Package bson_test — a program that imports this package and no other codec:
// it reads and writes BSON directly and through the registry, names the value
// types, is refused every other format, and links no third-party library.
package bson_test

import (
	"errors"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/app/config"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// shipment is a struct the round trip encodes.
type shipment struct {
	// ID is an ObjectID.
	ID bson.ObjectID `bson:"_id"`
	// Placed is a date.
	Placed time.Time `bson:"placed"`
	// Total is a decimal.
	Total bson.Decimal128 `bson:"total"`
	// Lines is an array of documents.
	Lines []line `bson:"lines"`
}

// line is one element of an array.
type line struct {
	// SKU is a string.
	SKU string `bson:"sku"`
	// Qty is a small integer.
	Qty int `bson:"qty"`
}

// TestRoundTrip pins Marshal, Append and Unmarshal through the package's own
// functions, with the value types it names.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	id, err := bson.ObjectIDFromHex("651020300102030405060708")
	if err != nil {
		t.Fatalf("ObjectIDFromHex = %v", err)
	}
	total, err := bson.ParseDecimal128("19.90")
	if err != nil {
		t.Fatalf("ParseDecimal128 = %v", err)
	}
	in := shipment{ID: id, Placed: time.Date(2026, time.October, 3, 9, 30, 0, 0, time.UTC), Total: total, Lines: []line{{SKU: "k-1", Qty: 2}}}
	data, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	appended, err := bson.Append([]byte("prefix"), in)
	//: Append writes the same document after the caller's bytes.
	if err != nil || string(appended) != "prefix"+string(data) {
		t.Fatalf("Append = %q, %v", appended, err)
	}
	var out shipment
	if err := bson.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal = %v", err)
	}
	//: every field, the decimal by its string form.
	if out.ID != in.ID || !out.Placed.Equal(in.Placed) || out.Total.String() != "19.90" || len(out.Lines) != 1 || out.Lines[0] != in.Lines[0] {
		t.Errorf("round trip turned %+v into %+v", in, out)
	}
	var generic map[string]any
	if err := bson.Unmarshal(data, &generic); err != nil {
		t.Fatalf("Unmarshal(map) = %v", err)
	}
	//: an interface gets the value types.
	if _, ok := generic["_id"].(bson.ObjectID); !ok {
		t.Errorf("_id decoded as %T, want bson.ObjectID", generic["_id"])
	}
	if _, ok := generic["placed"].(bson.DateTime); !ok {
		t.Errorf("placed decoded as %T, want bson.DateTime", generic["placed"])
	}
	if _, ok := generic["lines"].(bson.A); !ok {
		t.Errorf("lines decoded as %T, want bson.A", generic["lines"])
	}
}

// TestRefusalsCarryTheCodes pins the re-exported codes and sentinels.
func TestRefusalsCarryTheCodes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		err      func() error
		code     errs.Code
		sentinel error
	}
	tests := []tc{
		{"a scalar at the top level", func() error { _, err := bson.Marshal(42); return err }, bson.CodeMarshalFailed, bson.MarshalFailed},
		{"malformed input", func() error { return bson.Unmarshal([]byte{1, 2, 3}, new(any)) }, bson.CodeUnmarshalFailed, bson.UnmarshalFailed},
		{"a malformed ObjectID", func() error { _, err := bson.ObjectIDFromHex("nope"); return err }, bson.CodeValueInvalid, bson.ValueInvalid},
		{"an inexact decimal", func() error { _, err := bson.ParseDecimal128("1E-6177"); return err }, bson.CodeValueInvalid, bson.ValueInvalid},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := c.err()
		if !errs.HasCode(err, c.code) {
			t.Fatalf("err = %v, want code %v", err, c.code)
		}
		//: the sentinel matches too.
		if !errors.Is(err, c.sentinel) {
			t.Errorf("errors.Is(%v, sentinel) = false", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestItReadsItsFormatThroughTheRegistry decodes a BSON document through
// config.FSSource, and is refused a YAML one, which nothing registered.
func TestItReadsItsFormatThroughTheRegistry(t *testing.T) {
	t.Parallel()
	doc, err := bson.Marshal(bson.D{{Key: "name", Value: "kit"}, {Key: "port", Value: int32(4000)}})
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	tree := fstest.MapFS{
		"app.bson": {Data: doc},
		"app.yaml": {Data: []byte("name: kit\n")},
	}
	values, err := config.FSSource(tree, bson.Format, "app.bson").Load()
	//: the format this package registers.
	if err != nil {
		t.Fatalf("FSSource(bson).Load() error = %v", err)
	}
	//: the document's value, decoded.
	if values["name"] != "kit" {
		t.Errorf("FSSource(bson).Load() = %v, want name=kit", values)
	}
	_, err = config.FSSource(tree, "yaml", "app.yaml").Load()
	//: a format no import registered is refused as such.
	if !errors.Is(err, config.SourceFailed) {
		t.Fatalf("FSSource(yaml).Load() error = %v, want SourceFailed", err)
	}
}

// TestItRegistersItsFormatAlone reads the registry this test binary ends up
// with: this package's format and nothing else.
func TestItRegistersItsFormatAlone(t *testing.T) {
	t.Parallel()
	//: exactly one format, and it is this one.
	if got := corecodec.Available(); !slices.Equal(got, []corecodec.Format{bson.Format}) {
		t.Fatalf("registered formats = %v, want only %q", got, bson.Format)
	}
}

// TestGoListDepsNamesNoOtherCodec asks the go tool what this package depends
// on: the codec registry, this format's own packages, nothing of another
// codec, and nothing outside the SDK and the standard library. It needs the go
// tool, which a Bazel sandbox does not have.
func TestGoListDepsNamesNoOtherCodec(t *testing.T) {
	t.Parallel()
	goTool, err := exec.LookPath("go")
	//: no go tool: the registry test stands alone.
	if err != nil {
		t.Skip("no go tool on PATH (a Bazel sandbox); TestItRegistersItsFormatAlone pins the registry")
	}
	output, err := exec.Command(goTool, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/kitsunium/sdk/pkg/v1/data/codec/bson").Output()
	//: the go tool answers from the module this test runs in.
	if err != nil {
		t.Skipf("go list could not run here: %v", err)
	}
	//: every non-standard package the facade depends on.
	for line := range strings.FieldsSeq(string(output)) {
		//: a package outside the SDK, or another format's.
		if !strings.HasPrefix(line, "github.com/kitsunium/sdk/") || isAnotherCodec(line) {
			t.Errorf("go list -deps names %s", line)
		}
	}
}

// isAnotherCodec reports whether an import path is a codec package other than
// this format's service package and its core mirror; the registry and the
// shared scratch threshold are every codec's.
func isAnotherCodec(path string) bool {
	for _, layer := range []string{"github.com/kitsunium/sdk/internal/service/data/codec/", "github.com/kitsunium/sdk/internal/core/data/codec/"} {
		rest, found := strings.CutPrefix(path, layer)
		//: under a codec tree, and neither this format nor the shared scratch.
		if found && rest != "bson" && rest != "scratch" {
			return true
		}
	}
	//: not a codec package, or this format's own.
	return false
}

// TestItLinksNoModuleOutsideTheSDK reads the modules this test binary was
// linked from: the SDK's own, and nothing else — the gate ADR 0156 §1 states.
// Bazel builds without module information, which only `go test` records;
// TestItRegistersItsFormatAlone holds under both.
func TestItLinksNoModuleOutsideTheSDK(t *testing.T) {
	t.Parallel()
	info, ok := debug.ReadBuildInfo()
	//: no module information: built by Bazel.
	if !ok || len(info.Deps) == 0 {
		t.Skip("built without module information (Bazel's rules_go); TestItRegistersItsFormatAlone pins the registry under both")
	}
	//: every module linked.
	for _, dependency := range info.Deps {
		//: the SDK's own modules, and only those.
		if dependency.Path != "github.com/kitsunium/sdk" && !strings.HasPrefix(dependency.Path, "github.com/kitsunium/sdk/") {
			t.Errorf("the program links %s", dependency.Path)
		}
	}
}
