package xml_test

import (
	stdxml "encoding/xml"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/xml"
)

// fuzzDoc is a typed target with attributes, character data and nesting.
type fuzzDoc struct {
	XMLName stdxml.Name `xml:"doc"`
	ID      string      `xml:"id,attr"`
	Title   string      `xml:"title"`
	Items   []string    `xml:"items>item"`
	Count   int         `xml:"count"`
	Note    string      `xml:",comment"`
}

// FuzzUnmarshal is #897's xml target: any input decodes without a panic;
// what decodes re-encodes, and the second round trip is a fixed point.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range []string{
		`<doc id="7"><title>t</title><items><item>a</item><item>b</item></items><count>3</count></doc>`,
		`<?xml version="1.0"?><doc><!--c--><title>&lt;&amp;</title></doc>`,
		`<doc/>`, `<doc>`, `<!DOCTYPE doc [<!ENTITY a "b">]><doc>&a;</doc>`, ``,
	} {
		f.Add([]byte(seed))
	}
	c := xml.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var doc fuzzDoc
		//: decoded, or refused — never a panic.
		if err := c.Unmarshal(data, &doc); err != nil {
			return
		}
		out, err := c.Marshal(doc)
		//: encoding/xml refuses a comment holding "--": the decoder never reads one.
		if err != nil {
			t.Fatalf("Marshal of a decoded document: %v", err)
		}
		var again fuzzDoc
		//: what it wrote reads back as the same document.
		if err := c.Unmarshal(out, &again); err != nil {
			t.Fatalf("Unmarshal of %q: %v", out, err)
		}
		again.XMLName, doc.XMLName = stdxml.Name{}, stdxml.Name{}
		if !reflect.DeepEqual(again, doc) {
			t.Fatalf("round trip of %q: %#v, want %#v", out, again, doc)
		}
	})
}
