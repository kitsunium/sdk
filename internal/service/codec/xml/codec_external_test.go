package xml_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/xml"
)

type sampleDoc struct {
	ID string `xml:"id,attr"`
}

func TestNew(t *testing.T) {
	t.Parallel()
	c := xml.New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Name() != "xml" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   sampleDoc
		want string
	}
	tests := []tc{
		{"attribute-only", sampleDoc{ID: "x"}, `<sampleDoc id="x"></sampleDoc>`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data, err := xml.New().Marshal(c.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v", c.name, err)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("%s: %q missing %q", c.name, data, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		wantErr bool
	}
	tests := []tc{
		{"valid", `<sampleDoc id="y"/>`, false},
		{"malformed", `<sampleDoc`, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got sampleDoc
		err := xml.New().Unmarshal([]byte(c.data), &got)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if c.wantErr && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: missing UNMARSHAL_FAILED reason: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	if _, ok := codec.Lookup(codec.Format("xml")); !ok {
		t.Error("xml codec not registered")
	}
	if _, ok := codec.LookupMIME("application/xml"); !ok {
		t.Error("application/xml not resolved")
	}
	if _, ok := codec.LookupExt(".xml"); !ok {
		t.Error(".xml not resolved")
	}
}
