package yaml_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// benchLargeItems is how many records the large fixture carries: a page of a
// catalogue, large enough that per-node cost dominates the per-call overhead
// and small enough to stay far below the 10 MiB input cap.
const benchLargeItems int = 500

// benchSmallDoc is the small fixture's document, written by hand so every
// implementation benchmarked against these fixtures decodes the same bytes.
const benchSmallDoc string = "name: kitsune\nport: 8080\ndebug: true\n"

// benchMediumDoc is the medium fixture's document.
const benchMediumDoc string = `# a service configuration
service: catalogue
server:
  host: 0.0.0.0
  port: 8443
  tls:
    cert: /etc/tls/server.crt
    key: /etc/tls/server.key
database:
  dsn: "postgres://app@db:5432/catalogue?sslmode=verify-full"
  max_conns: 32
  ratio: 0.75
features: [search, export, audit, webhooks, sso, quotas, billing, metrics]
limits:
  requests: 1000
  burst: 50
  uploads: 10
  exports: 5
  users: 250
  projects: 40
  tokens: 20
  hooks: 15
description: |
  The catalogue service.
  It serves the product list and its search.
`

// benchSinkBytes observes every encode result so the compiler cannot delete
// the call the benchmark exists to time.
var benchSinkBytes []byte

// benchSinkAny observes every decode target.
var benchSinkAny any

// benchLimitNames and benchLimitValues are the medium fixture's map, in the
// document's order.
var (
	benchLimitNames  = []string{"requests", "burst", "uploads", "exports", "users", "projects", "tokens", "hooks"}
	benchLimitValues = []int{1000, 50, 10, 5, 250, 40, 20, 15}
)

// benchSmall is the smallest configuration a program reads: three scalars.
type benchSmall struct {
	// Name is a plain string.
	Name string `yaml:"name"`
	// Port is an integer.
	Port int `yaml:"port"`
	// Debug is a boolean.
	Debug bool `yaml:"debug"`
}

// benchTLS is a nested section of benchMedium.
type benchTLS struct {
	// Cert is a path.
	Cert string `yaml:"cert"`
	// Key is a path.
	Key string `yaml:"key"`
}

// benchServer is a nested section of benchMedium.
type benchServer struct {
	// Host is a plain string.
	Host string `yaml:"host"`
	// TLS is a nested struct.
	TLS benchTLS `yaml:"tls"`
	// Port is an integer.
	Port int `yaml:"port"`
}

// benchDatabase is a nested section of benchMedium.
type benchDatabase struct {
	// DSN is a string that needs quoting (it carries ": ").
	DSN string `yaml:"dsn"`
	// MaxConns is an integer.
	MaxConns int `yaml:"max_conns"`
	// Ratio is a float.
	Ratio float64 `yaml:"ratio"`
}

// benchMedium is a realistic service configuration: nested sections, a list,
// a map and a multi-line description.
type benchMedium struct {
	// Limits is a map of integers.
	Limits map[string]int `yaml:"limits"`
	// Service names the program.
	Service string `yaml:"service"`
	// Description is a multi-line string.
	Description string `yaml:"description"`
	// Server is a nested section.
	Server benchServer `yaml:"server"`
	// Database is a nested section.
	Database benchDatabase `yaml:"database"`
	// Features is a list of strings.
	Features []string `yaml:"features"`
}

// benchItem is one record of benchLarge.
type benchItem struct {
	// Name is a string.
	Name string `yaml:"name"`
	// Tags is a short list.
	Tags []string `yaml:"tags"`
	// ID is an integer.
	ID int `yaml:"id"`
	// Score is a float.
	Score float64 `yaml:"score"`
	// Active is a boolean.
	Active bool `yaml:"active"`
}

// benchLarge is a catalogue: benchLargeItems records.
type benchLarge struct {
	// Items is the record list.
	Items []benchItem `yaml:"items"`
}

// benchSmallValue returns the small fixture as a Go value.
func benchSmallValue() benchSmall {
	//: three scalars.
	return benchSmall{Name: "kitsune", Port: 8080, Debug: true}
}

// benchMediumValue returns the medium fixture as a Go value.
func benchMediumValue() benchMedium {
	//: the map, from its two halves.
	limits := make(map[string]int, len(benchLimitNames))
	//: one entry per name.
	for i, name := range benchLimitNames {
		//: the value at the same index.
		limits[name] = benchLimitValues[i]
	}
	//: the same content as benchMediumDoc.
	return benchMedium{
		Service: "catalogue",
		Server:  benchServer{Host: "0.0.0.0", Port: 8443, TLS: benchTLS{Cert: "/etc/tls/server.crt", Key: "/etc/tls/server.key"}},
		Database: benchDatabase{
			DSN: "postgres://app@db:5432/catalogue?sslmode=verify-full", MaxConns: 32, Ratio: 0.75,
		},
		Features:    []string{"search", "export", "audit", "webhooks", "sso", "quotas", "billing", "metrics"},
		Limits:      limits,
		Description: "The catalogue service.\nIt serves the product list and its search.\n",
	}
}

// benchLargeValue returns the large fixture as a Go value.
func benchLargeValue() benchLarge {
	//: one record per index, every field derived from it.
	items := make([]benchItem, 0, benchLargeItems)
	//: build the records.
	for i := range benchLargeItems {
		//: deterministic content.
		items = append(items, benchItem{
			ID: i, Name: "item-" + strconv.Itoa(i), Tags: []string{"alpha", "beta", "gamma"},
			Score: float64(i) + 0.5, Active: i%2 == 0,
		})
	}
	//: the catalogue.
	return benchLarge{Items: items}
}

// benchLargeDoc renders the large fixture's document by hand, in the block
// style a person writes, so every implementation decodes the same bytes.
func benchLargeDoc() string {
	//: one block-sequence entry per record.
	var b strings.Builder
	//: the root key.
	b.WriteString("items:\n")
	//: the records.
	for i := range benchLargeItems {
		//: one record.
		b.WriteString("  - id: " + strconv.Itoa(i) + "\n")
		b.WriteString("    name: item-" + strconv.Itoa(i) + "\n")
		b.WriteString("    tags: [alpha, beta, gamma]\n")
		b.WriteString("    score: " + strconv.Itoa(i) + ".5\n")
		b.WriteString("    active: " + strconv.FormatBool(i%2 == 0) + "\n")
	}
	//: the document.
	return b.String()
}

// benchMarshal returns the benchmark body encoding value.
func benchMarshal(c codec.Codec, value any) func(*testing.B) {
	//: one encode per iteration.
	return func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := c.Marshal(value)
			if err != nil {
				b.Fatalf("Marshal: %v", err)
			}
			benchSinkBytes = out
		}
	}
}

// benchUnmarshal returns the benchmark body decoding data into a fresh target.
func benchUnmarshal(c codec.Codec, data []byte, target func() any) func(*testing.B) {
	//: one decode per iteration, into a target allocated inside the loop.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			into := target()
			if err := c.Unmarshal(data, into); err != nil {
				b.Fatalf("Unmarshal: %v", err)
			}
			benchSinkAny = into
		}
	}
}

// BenchmarkMarshal measures encoding each fixture.
func BenchmarkMarshal(b *testing.B) {
	type tc struct {
		value any
		name  string
	}
	tests := []tc{
		{name: "small", value: benchSmallValue()},
		{name: "medium", value: benchMediumValue()},
		{name: "large", value: benchLargeValue()},
	}
	for _, tc := range tests {
		b.Run(tc.name, benchMarshal(yaml.New(), tc.value))
	}
}

// BenchmarkUnmarshalStruct measures decoding each fixture's document into its
// Go type.
func BenchmarkUnmarshalStruct(b *testing.B) {
	type tc struct {
		target func() any
		name   string
		doc    string
	}
	tests := []tc{
		{name: "small", doc: benchSmallDoc, target: func() any { return new(benchSmall) }},
		{name: "medium", doc: benchMediumDoc, target: func() any { return new(benchMedium) }},
		{name: "large", doc: benchLargeDoc(), target: func() any { return new(benchLarge) }},
	}
	for _, tc := range tests {
		b.Run(tc.name, benchUnmarshal(yaml.New(), []byte(tc.doc), tc.target))
	}
}

// BenchmarkUnmarshalAny measures decoding each fixture's document into a
// map[string]any — the path config and i18n take.
func BenchmarkUnmarshalAny(b *testing.B) {
	type tc struct {
		name string
		doc  string
	}
	tests := []tc{
		{name: "small", doc: benchSmallDoc},
		{name: "medium", doc: benchMediumDoc},
		{name: "large", doc: benchLargeDoc()},
	}
	for _, tc := range tests {
		b.Run(tc.name, benchUnmarshal(yaml.New(), []byte(tc.doc), func() any { return &map[string]any{} }))
	}
}
