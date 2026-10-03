// Package toml_test — a program that imports this package and no other
// codec: it reads TOML through the registry, is refused every other format,
// and links no module outside the SDK — the TOML codec is the SDK's own.
package toml_test

import (
	"errors"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/app/config"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestItReadsItsFormatThroughTheRegistry decodes a TOML document through
// config.FSSource — the path a framework reading its embedded configuration
// takes — and is refused a BSON one, which nothing registered.
func TestItReadsItsFormatThroughTheRegistry(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"app.toml": {Data: []byte("name = \"kit\"\nport = 4000\n")},
		"app.bson": {Data: []byte{0x05, 0x00, 0x00, 0x00, 0x00}},
	}
	values, err := config.FSSource(tree, toml.Format, "app.toml").Load()
	//: the format this package registers.
	if err != nil {
		t.Fatalf("FSSource(toml).Load() error = %v", err)
	}
	//: the document's value, decoded.
	if values["name"] != "kit" {
		t.Errorf("FSSource(toml).Load() = %v, want name=kit", values)
	}
	_, err = config.FSSource(tree, "bson", "app.bson").Load()
	//: a format no import registered is refused as such.
	if !errors.Is(err, config.SourceFailed) {
		t.Fatalf("FSSource(bson).Load() error = %v, want SourceFailed", err)
	}
}

// TestItRegistersItsFormatAlone reads the registry this test binary ends up
// with: this package's format and nothing else, since nothing else imported a
// codec.
func TestItRegistersItsFormatAlone(t *testing.T) {
	t.Parallel()
	//: exactly one format, and it is this one.
	if got := corecodec.Available(); !slices.Equal(got, []corecodec.Format{toml.Format}) {
		t.Fatalf("registered formats = %v, want only %q", got, toml.Format)
	}
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

// TestLocalTypesAreTheDecodedOnes decodes the three local kinds through the
// registry, as config and i18n do, and finds this package's types.
func TestLocalTypesAreTheDecodedOnes(t *testing.T) {
	t.Parallel()
	c, ok := corecodec.Lookup(toml.Format)
	//: the format this package registers.
	if !ok {
		t.Fatal("toml is not registered")
	}
	var got map[string]any
	//: one of each kind.
	if err := c.Unmarshal([]byte("d = 1979-05-27\nt = 07:32:00\ndt = 1979-05-27T07:32:00\n"), &got); err != nil {
		t.Fatal(err)
	}
	_, isDate := got["d"].(toml.LocalDate)
	_, isTime := got["t"].(toml.LocalTime)
	_, isDateTime := got["dt"].(toml.LocalDateTime)
	//: each is the facade's type.
	if !isDate || !isTime || !isDateTime {
		t.Errorf("decoded %#v", got)
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
	output, err := exec.Command(goTool, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/kitsunium/sdk/pkg/v1/data/codec/toml").Output()
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
		if found && rest != "toml" && rest != "scratch" {
			return true
		}
	}
	//: not a codec package, or this format's own.
	return false
}

// TestARefusalCarriesTheNamedCode pins that the facade's code and sentinel are
// the ones the codec refuses with.
func TestARefusalCarriesTheNamedCode(t *testing.T) {
	t.Parallel()
	c, ok := corecodec.Lookup(toml.Format)
	//: the format this package registers.
	if !ok {
		t.Fatal("toml is not registered")
	}
	err := c.Unmarshal([]byte("= nope"), new(map[string]any))
	//: the code the facade names.
	if !errs.HasCode(err, toml.CodeUnmarshalFailed) {
		t.Fatalf("err = %v, want code %v", err, toml.CodeUnmarshalFailed)
	}
	//: and the sentinel bound to it.
	if !errors.Is(err, toml.UnmarshalFailed) {
		t.Errorf("errors.Is(%v, sentinel) = false", err)
	}
}
