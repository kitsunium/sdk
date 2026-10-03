// Package strictjson_test walks the facade as a program decoding a document
// uses it, and asks the go tool what the decoder links.
package strictjson_test

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// createItem is the document the facade test decodes.
type createItem struct {
	Name  string `json:"name"`
	Price int    `json:"price"`
}

// TestTheFacadeDecodesAndRefuses walks the public surface: a good document
// decodes, a bad one is refused with a status and a location, and nothing
// needs an internal import.
func TestTheFacadeDecodesAndRefuses(t *testing.T) {
	t.Parallel()
	var in createItem
	if err := strictjson.Decode(strings.NewReader(`{"name":"lamp","price":12}`), &in, 1<<10); err != nil || in.Name != "lamp" {
		t.Fatalf("Decode() = %v, %+v; want the document", err, in)
	}
	err := strictjson.Decode(strings.NewReader(`{"name":"lamp","role":"admin"}`), &in, 1<<10)
	if !errs.HasCode(err, strictjson.CodeMemberUnknown) || errs.HTTPStatusOf(err) != http.StatusBadRequest {
		t.Fatalf("Decode() = %v (status %d), want MEMBER_UNKNOWN and 400", err, errs.HTTPStatusOf(err))
	}
	if pointer, located := strictjson.PointerOf(err); !located || pointer != "/role" {
		t.Errorf("PointerOf() = %q, %v; want /role", pointer, located)
	}
	if strings.Contains(errs.PublicOf(err), "admin") {
		t.Errorf("the refusal repeated a value: %q", errs.PublicOf(err))
	}
}

// TestTheDecoderLinksNoHTTP asks the go tool what this package depends on:
// net/http is the httpbody package's, and a program decoding documents that
// are not request bodies does not link it. It needs the go tool, which a
// Bazel sandbox does not have.
func TestTheDecoderLinksNoHTTP(t *testing.T) {
	t.Parallel()
	goTool, err := exec.LookPath("go")
	//: no go tool: nothing to ask.
	if err != nil {
		t.Skip("no go tool on PATH (a Bazel sandbox)")
	}
	output, err := exec.Command(goTool, "list", "-deps", "github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson").Output()
	//: the go tool answers from the module this test runs in.
	if err != nil {
		t.Skipf("go list could not run here: %v", err)
	}
	//: every package the decoder depends on.
	for line := range strings.FieldsSeq(string(output)) {
		//: an HTTP package, or the HTTP form of this decoder.
		if line == "net/http" || strings.HasSuffix(line, "/strictjson/httpbody") {
			t.Errorf("go list -deps names %s", line)
		}
	}
}
