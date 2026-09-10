// Package hcl_test measures the quarantined HCL codec against the two in-tree
// codecs a consumer gets by default, JSON and CBOR (internal/service/codec/*).
// All three implement the same core/codec.Codec port, and every row below
// encodes the SAME Go value — the fixtures carry `hcl` and `json` struct tags
// side by side, and fxamacker/cbor falls back to the `json` tag — so the
// comparison is format against format with the data held constant. HCL is a
// configuration language, so the fixtures are configuration documents rather
// than the record shapes pkg/v1/codec/BENCH.md uses.
package hcl_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
	sdkjson "github.com/kitsunium/sdk/internal/service/codec/json"
	"github.com/kitsunium/sdk/third-party/codec/hcl"
)

const (
	// benchMediumListeners is the block count in the medium fixture — a
	// service with a plaintext and a TLS listener, which is the shape a real
	// deployment file carries.
	benchMediumListeners int = 2
	// benchLargeServices is the block count in the large fixture. 64 blocks is
	// a fleet manifest: big enough that per-block cost dominates the per-call
	// cost, small enough to stay a plausible hand-edited file.
	benchLargeServices int = 64
	// benchAppendCap pre-sizes the Append destination so the row measures the
	// encoder rather than the allocator growing a buffer.
	benchAppendCap int = 64 << 10
)

// benchWire is a package-level sink for encoded bytes so the compiler cannot
// elide the encode being measured.
var benchWire []byte

// benchDecoded is a package-level sink for decoded documents.
var benchDecoded any

// listener is one nested HCL block: an address the service binds.
type listener struct {
	Addr string `hcl:"addr" json:"addr"`
	Port int    `hcl:"port" json:"port"`
	TLS  bool   `hcl:"tls" json:"tls"`
}

// service is one repeated block in the large fixture.
type service struct {
	Name    string `hcl:"name" json:"name"`
	Image   string `hcl:"image" json:"image"`
	Replica int    `hcl:"replica" json:"replica"`
	Enabled bool   `hcl:"enabled" json:"enabled"`
}

// smallDoc is the two-attribute fixture: the smallest document that is still a
// document, and the shape the package's own round-trip test uses.
type smallDoc struct {
	Name    string `hcl:"name" json:"name"`
	Replica int    `hcl:"replica" json:"replica"`
}

// mediumDoc is six attributes plus two nested blocks — an ordinary service
// configuration file.
type mediumDoc struct {
	Name      string     `hcl:"name" json:"name"`
	Region    string     `hcl:"region" json:"region"`
	Replica   int        `hcl:"replica" json:"replica"`
	Timeout   int        `hcl:"timeout" json:"timeout"`
	Debug     bool       `hcl:"debug" json:"debug"`
	Version   string     `hcl:"version" json:"version"`
	Listeners []listener `hcl:"listener,block" json:"listener"`
}

// largeDoc is one attribute plus 64 repeated blocks — a fleet manifest.
type largeDoc struct {
	Cluster  string    `hcl:"cluster" json:"cluster"`
	Services []service `hcl:"service,block" json:"service"`
}

// benchDoc names one fixture and pairs it with a fresh decode target, since
// HCL, JSON and CBOR all decode into a pointer to the same Go type.
type benchDoc struct {
	name  string
	value any
	fresh func() any
}

// benchCodec names one codec under test.
type benchCodec struct {
	name  string
	codec corecodec.Codec
}

// smallFixture builds the two-attribute document.
func smallFixture() smallDoc {
	//: deliberately unremarkable values — content does not change an encoder's
	//: cost, and a fixed fixture makes the wire-size report reproducible.
	return smallDoc{Name: "kitsunium", Replica: 3}
}

// mediumFixture builds the six-attribute, two-block document.
func mediumFixture() mediumDoc {
	//: two listeners is the plaintext + TLS pair a real service declares.
	ls := make([]listener, 0, benchMediumListeners)
	ls = append(ls, listener{Addr: "0.0.0.0", Port: 8080, TLS: false})
	ls = append(ls, listener{Addr: "0.0.0.0", Port: 8443, TLS: true})
	return mediumDoc{
		Name:      "billing",
		Region:    "eu-west-1",
		Replica:   3,
		Timeout:   30,
		Debug:     false,
		Version:   "v1.42.0",
		Listeners: ls,
	}
}

// largeFixture builds the 64-block fleet manifest.
func largeFixture() largeDoc {
	//: every block differs only in its index, so the encoder sees 64 distinct
	//: strings rather than 64 chances to hit a string-interning fast path.
	svcs := make([]service, 0, benchLargeServices)
	for i := range benchLargeServices {
		svcs = append(svcs, service{
			Name:    "service-" + string(rune('a'+i%26)) + string(rune('0'+i%10)),
			Image:   "registry.example.com/kitsunium/svc:v1." + string(rune('0'+i%10)),
			Replica: i%7 + 1,
			Enabled: i%3 != 0,
		})
	}
	return largeDoc{Cluster: "prod-eu", Services: svcs}
}

// benchDocs is the size ladder every row walks.
func benchDocs() []benchDoc {
	//: each entry carries a constructor for a FRESH decode target, because a
	//: decode into a reused struct would measure a partly-populated value.
	return []benchDoc{
		{"small", smallFixture(), func() any { return new(smallDoc) }},
		{"medium", mediumFixture(), func() any { return new(mediumDoc) }},
		{"large", largeFixture(), func() any { return new(largeDoc) }},
	}
}

// benchCodecs is HCL plus the two in-tree references.
func benchCodecs() []benchCodec {
	//: json and cbor are the codecs a consumer gets without opting into this
	//: package's dependency graph — they are the bar HCL has to be judged at.
	return []benchCodec{
		{"hcl", hcl.New()},
		{"json", sdkjson.New()},
		{"cbor", cbor.New()},
	}
}

// TestReportWireSize records the encoded size of each fixture under each codec.
// Byte count is the axis `go test -bench` cannot report and the one that decides
// whether a config format is cheap to ship, so it is measured in the ordinary
// suite exactly as third-party/transform reports its compression ratios.
func TestReportWireSize(t *testing.T) {
	t.Parallel()
	for _, doc := range benchDocs() {
		for _, c := range benchCodecs() {
			out, err := c.codec.Marshal(doc.value)
			if err != nil {
				t.Fatalf("%s.Marshal(%s): %v", c.name, doc.name, err)
			}
			t.Logf("wire-size %-6s %-6s = %d bytes", doc.name, c.name, len(out))
		}
	}
}

// BenchmarkMarshal encodes each fixture with each codec.
func BenchmarkMarshal(b *testing.B) {
	for _, c := range benchCodecs() {
		for _, doc := range benchDocs() {
			b.Run(c.name+"/"+doc.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					out, err := c.codec.Marshal(doc.value)
					if err != nil {
						b.Fatalf("Marshal: %v", err)
					}
					benchWire = out
				}
			})
		}
	}
}

// BenchmarkUnmarshal decodes bytes that the SAME codec produced, into a fresh
// target every iteration.
func BenchmarkUnmarshal(b *testing.B) {
	for _, c := range benchCodecs() {
		for _, doc := range benchDocs() {
			b.Run(c.name+"/"+doc.name, func(b *testing.B) {
				//: encoded inside the row so no loop-local is captured by this
				//: closure; b.Loop resets the timer, so the seed is untimed.
				seed, err := c.codec.Marshal(doc.value)
				if err != nil {
					b.Fatalf("seed Marshal: %v", err)
				}
				b.ReportAllocs()
				for b.Loop() {
					dst := doc.fresh()
					if uerr := c.codec.Unmarshal(seed, dst); uerr != nil {
						b.Fatalf("Unmarshal: %v", uerr)
					}
					benchDecoded = dst
				}
			})
		}
	}
}

// BenchmarkAppend measures the optional codec.Appender path into a buffer that
// already has capacity. It is the row that shows whether a codec can encode into
// a caller's buffer or merely copies into it afterwards.
func BenchmarkAppend(b *testing.B) {
	for _, c := range benchCodecs() {
		//: a codec that does not implement the optional Appender has no row.
		if _, ok := c.codec.(corecodec.Appender); !ok {
			continue
		}
		for _, doc := range benchDocs() {
			b.Run(c.name+"/"+doc.name, func(b *testing.B) {
				//: re-asserted inside the row rather than captured from the
				//: loop body, and checked rather than discarded.
				appender, ok := c.codec.(corecodec.Appender)
				if !ok {
					b.Fatalf("%s stopped implementing codec.Appender", c.name)
				}
				dst := make([]byte, 0, benchAppendCap)
				b.ReportAllocs()
				for b.Loop() {
					out, err := appender.Append(dst[:0], doc.value)
					if err != nil {
						b.Fatalf("Append: %v", err)
					}
					benchWire = out
				}
			})
		}
	}
}
