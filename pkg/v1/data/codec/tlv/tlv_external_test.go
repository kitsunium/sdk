// Package tlv_test — a program that imports this package and no other
// codec: it reads and writes its format through the registry, is refused
// with the codes the package names, and links nothing outside the SDK and
// the standard library.
package tlv_test

import (
	"errors"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// formats are the names this package registers, sorted as the registry lists them.
var formats = []string{tlv.Format}

// record is the value the round trip encodes.
type record struct {
	// Name is a string.
	Name string
	// Port is an integer.
	Port int
}

// marshal encodes v through the registry under format.
func marshal(t *testing.T, format string, v any) ([]byte, error) {
	t.Helper()
	c, ok := corecodec.Lookup(corecodec.Format(format))
	//: the import registered the format.
	if !ok {
		t.Fatalf("%s is not registered", format)
	}
	//: the registered codec's own encoder.
	return c.Marshal(v)
}

// unmarshal decodes data through the registry under format.
func unmarshal(t *testing.T, format string, data []byte, v any) error {
	t.Helper()
	c, ok := corecodec.Lookup(corecodec.Format(format))
	//: the import registered the format.
	if !ok {
		t.Fatalf("%s is not registered", format)
	}
	//: the registered codec's own decoder.
	return c.Unmarshal(data, v)
}

// roundTrip encodes in and decodes it into out, failing the test on either error.
func roundTrip(t *testing.T, format string, in, out any) {
	t.Helper()
	data, err := marshal(t, format, in)
	//: the value encodes.
	if err != nil {
		t.Fatalf("%s Marshal = %v", format, err)
	}
	//: and decodes.
	if err := unmarshal(t, format, data, out); err != nil {
		t.Fatalf("%s Unmarshal = %v", format, err)
	}
}

// TestItReadsAndWritesItsFormat round-trips a value through the registry,
// which nothing but this package's import filled.
func TestItReadsAndWritesItsFormat(t *testing.T) {
	t.Parallel()
	in := record{Name: "kit", Port: 4000}
	var out record
	roundTrip(t, tlv.Format, in, &out)
	//: the value read back is the value written.
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

// TestARefusalCarriesTheNamedCode pins that the facade's code and sentinel are
// the ones the codec refuses with.
func TestARefusalCarriesTheNamedCode(t *testing.T) {
	t.Parallel()
	refuse := func() error { _, err := marshal(t, tlv.Format, make(chan int)); return err }
	err := refuse()
	//: the code the facade names.
	if !errs.HasCode(err, tlv.CodeUnsupportedType) {
		t.Fatalf("err = %v, want code %v", err, tlv.CodeUnsupportedType)
	}
	//: and the sentinel bound to it.
	if !errors.Is(err, tlv.UnsupportedType) {
		t.Errorf("errors.Is(%v, sentinel) = false", err)
	}
}

// TestItRegistersItsFormatAlone reads the registry this test binary ends up
// with: this package's formats and nothing else, since nothing else imported a
// codec.
func TestItRegistersItsFormatAlone(t *testing.T) {
	t.Parallel()
	want := make([]corecodec.Format, 0, len(formats))
	//: the names this package publishes, as the registry types them.
	for _, format := range formats {
		want = append(want, corecodec.Format(format))
	}
	//: exactly this package's formats.
	if got := corecodec.Available(); !slices.Equal(got, want) {
		t.Fatalf("registered formats = %v, want only %v", got, want)
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
	output, err := exec.Command(goTool, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/kitsunium/sdk/pkg/v1/data/codec/tlv").Output()
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
		if found && rest != "tlv" && rest != "scratch" {
			return true
		}
	}
	//: not a codec package, or this format's own.
	return false
}
