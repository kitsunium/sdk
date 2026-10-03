package toml_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/toml"
)

// benchConfigDoc is a configuration file shaped like the ones config.FileSource
// and config.FSSource read: top-level keys, an inline table, nested standard
// tables, an array of tables, every scalar family and a date-time.
const benchConfigDoc = `# service configuration
title = "kitsunium"
version = 3
debug = false
ratio = 0.75
started = 2024-01-15T09:30:00Z
tags = ["alpha", "beta", "gamma"]
ports = [8080, 8081, 8082]
owner = { name = "Ada Lovelace", email = "ada@example.com" }

[server]
host = "0.0.0.0"
port = 8443
read_timeout = "5s"
write_timeout = "10s"
max_header_bytes = 1048576

[server.tls]
enabled = true
cert = "/etc/ssl/cert.pem"
key = "/etc/ssl/key.pem"
min_version = "1.2"

[database]
dsn = "postgres://localhost:5432/app"
max_open = 25
max_idle = 5
lifetime_seconds = 3600

[[database.replicas]]
dsn = "postgres://replica-1:5432/app"
weight = 1.5

[[database.replicas]]
dsn = "postgres://replica-2:5432/app"
weight = 0.5

[logging]
level = "info"
outputs = ["stderr", "file"]

[logging.file]
path = "/var/log/app.log"
max_size_mb = 100
max_backups = 7
compress = true

[features]
search = true
billing = false
beta-ui = true
`

// benchLargeItems is how many [[items]] the scaling document carries: enough
// that the per-table cost dominates the per-call overhead.
const benchLargeItems int = 1000

// benchWideKeys is how many keys the wide-table document puts in one table:
// enough for a per-key cost that grows with the table to show.
const benchWideKeys int = 10000

// benchAppendCap pre-sizes the Append destination so the benchmark measures
// the encoder rather than the allocator growing a buffer under it.
const benchAppendCap int = 1 << 14

// benchSinkBytes observes every encode result so the compiler cannot delete
// the call the benchmark exists to time.
var benchSinkBytes []byte

// benchSinkAny observes an untyped decode target.
var benchSinkAny any

// benchOwner is the inline table of benchConfig.
type benchOwner struct {
	// Name is a string with a space.
	Name string `toml:"name"`
	// Email is a plain string.
	Email string `toml:"email"`
}

// benchTLS is the [server.tls] table.
type benchTLS struct {
	// Enabled is a boolean.
	Enabled bool `toml:"enabled"`
	// Cert is a path.
	Cert string `toml:"cert"`
	// Key is a path.
	Key string `toml:"key"`
	// MinVersion is a dotted version string.
	MinVersion string `toml:"min_version"`
}

// benchServer is the [server] table.
type benchServer struct {
	// Host is an address.
	Host string `toml:"host"`
	// Port is an integer.
	Port int `toml:"port"`
	// ReadTimeout is a duration spelled as a string.
	ReadTimeout string `toml:"read_timeout"`
	// WriteTimeout is a duration spelled as a string.
	WriteTimeout string `toml:"write_timeout"`
	// MaxHeaderBytes is a wider integer.
	MaxHeaderBytes int64 `toml:"max_header_bytes"`
	// TLS is a nested table.
	TLS benchTLS `toml:"tls"`
}

// benchReplica is one element of [[database.replicas]].
type benchReplica struct {
	// DSN is a connection string.
	DSN string `toml:"dsn"`
	// Weight is a float.
	Weight float64 `toml:"weight"`
}

// benchDatabase is the [database] table.
type benchDatabase struct {
	// DSN is a connection string.
	DSN string `toml:"dsn"`
	// MaxOpen is an integer.
	MaxOpen int `toml:"max_open"`
	// MaxIdle is an integer.
	MaxIdle int `toml:"max_idle"`
	// LifetimeSeconds is an integer.
	LifetimeSeconds int `toml:"lifetime_seconds"`
	// Replicas is an array of tables.
	Replicas []benchReplica `toml:"replicas"`
}

// benchLogFile is the [logging.file] table.
type benchLogFile struct {
	// Path is a path.
	Path string `toml:"path"`
	// MaxSizeMB is an integer.
	MaxSizeMB int `toml:"max_size_mb"`
	// MaxBackups is an integer.
	MaxBackups int `toml:"max_backups"`
	// Compress is a boolean.
	Compress bool `toml:"compress"`
}

// benchLogging is the [logging] table.
type benchLogging struct {
	// Level is a string.
	Level string `toml:"level"`
	// Outputs is an array of strings.
	Outputs []string `toml:"outputs"`
	// File is a nested table.
	File benchLogFile `toml:"file"`
}

// benchConfig is benchConfigDoc as a typed target.
type benchConfig struct {
	// Title is a string.
	Title string `toml:"title"`
	// Version is an integer.
	Version int `toml:"version"`
	// Debug is a boolean.
	Debug bool `toml:"debug"`
	// Ratio is a float.
	Ratio float64 `toml:"ratio"`
	// Started is an offset date-time.
	Started time.Time `toml:"started"`
	// Tags is an array of strings.
	Tags []string `toml:"tags"`
	// Ports is an array of integers.
	Ports []int `toml:"ports"`
	// Owner is an inline table.
	Owner benchOwner `toml:"owner"`
	// Server is a standard table with a sub-table.
	Server benchServer `toml:"server"`
	// Database carries an array of tables.
	Database benchDatabase `toml:"database"`
	// Logging carries a sub-table.
	Logging benchLogging `toml:"logging"`
	// Features is a table decoded into a map.
	Features map[string]bool `toml:"features"`
}

// benchItem is one element of the scaling document.
type benchItem struct {
	// ID is an integer.
	ID int64 `toml:"id"`
	// Name is a string.
	Name string `toml:"name"`
	// Price is a float.
	Price float64 `toml:"price"`
	// Active is a boolean.
	Active bool `toml:"active"`
	// Labels is an array of strings.
	Labels []string `toml:"labels"`
}

// benchCatalogue is the scaling document as a typed target.
type benchCatalogue struct {
	// Items is the array of tables.
	Items []benchItem `toml:"items"`
}

// benchLargeDoc returns a document of benchLargeItems [[items]] tables.
func benchLargeDoc() []byte {
	var sb strings.Builder
	//: one table per item, five keys each.
	for i := range benchLargeItems {
		sb.WriteString("[[items]]\nid = ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\nname = \"item-")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\"\nprice = 12.5\nactive = true\nlabels = [\"a\", \"b\"]\n\n")
	}
	//: the whole document.
	return []byte(sb.String())
}

// benchWideDoc returns a document of benchWideKeys keys in the root table.
func benchWideDoc() []byte {
	var sb strings.Builder
	//: one key per line.
	for i := range benchWideKeys {
		sb.WriteString("key_")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(" = ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	//: the whole document.
	return []byte(sb.String())
}

// benchConfigValue decodes benchConfigDoc once into the typed target the
// encode benchmarks start from.
func benchConfigValue(b *testing.B) benchConfig {
	b.Helper()
	var cfg benchConfig
	//: a document the codec cannot read is a broken benchmark, not a result.
	if err := toml.New().Unmarshal([]byte(benchConfigDoc), &cfg); err != nil {
		b.Fatalf("seed Unmarshal: %v", err)
	}
	//: the seed value.
	return cfg
}

// BenchmarkUnmarshalConfigMap decodes the configuration document into the
// map[string]any every config source decodes into.
func BenchmarkUnmarshalConfigMap(b *testing.B) {
	data := []byte(benchConfigDoc)
	c := toml.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	//: the measured loop.
	for b.Loop() {
		out := make(map[string]any)
		//: a failure is a broken benchmark.
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		benchSinkAny = out
	}
}

// BenchmarkUnmarshalConfigStruct decodes the configuration document into its
// typed struct.
func BenchmarkUnmarshalConfigStruct(b *testing.B) {
	data := []byte(benchConfigDoc)
	c := toml.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	//: the measured loop.
	for b.Loop() {
		var out benchConfig
		//: a failure is a broken benchmark.
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		benchSinkAny = out
	}
}

// BenchmarkMarshalConfigStruct encodes the typed configuration.
func BenchmarkMarshalConfigStruct(b *testing.B) {
	cfg := benchConfigValue(b)
	c := toml.New()
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		out, err := c.Marshal(cfg)
		//: a failure is a broken benchmark.
		if err != nil {
			b.Fatal(err)
		}
		benchSinkBytes = out
	}
}

// BenchmarkMarshalConfigMap encodes the configuration as the map[string]any a
// decode produced.
func BenchmarkMarshalConfigMap(b *testing.B) {
	in := make(map[string]any)
	c := toml.New()
	//: the map form of the document.
	if err := c.Unmarshal([]byte(benchConfigDoc), &in); err != nil {
		b.Fatalf("seed Unmarshal: %v", err)
	}
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		out, err := c.Marshal(in)
		//: a failure is a broken benchmark.
		if err != nil {
			b.Fatal(err)
		}
		benchSinkBytes = out
	}
}

// BenchmarkAppendConfigStruct encodes the typed configuration onto a
// destination that already has capacity.
func BenchmarkAppendConfigStruct(b *testing.B) {
	cfg := benchConfigValue(b)
	appender, ok := toml.New().(corecodec.Appender)
	//: the codec has always implemented Appender.
	if !ok {
		b.Fatal("the TOML codec does not implement codec.Appender")
	}
	dst := make([]byte, 0, benchAppendCap)
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		out, err := appender.Append(dst[:0], cfg)
		//: a failure is a broken benchmark.
		if err != nil {
			b.Fatal(err)
		}
		benchSinkBytes = out
	}
}

// BenchmarkUnmarshalSmallMap decodes the three-key payload the allocation
// gate pins, into map[string]int.
func BenchmarkUnmarshalSmallMap(b *testing.B) {
	c := toml.New()
	data, err := c.Marshal(map[string]int{"a": 1, "b": 2, "c": 3})
	//: the seed payload.
	if err != nil {
		b.Fatalf("seed Marshal: %v", err)
	}
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		var out map[string]int
		//: a failure is a broken benchmark.
		if uerr := c.Unmarshal(data, &out); uerr != nil {
			b.Fatal(uerr)
		}
		benchSinkAny = out
	}
}

// BenchmarkMarshalSmallMap encodes the three-key payload the allocation gate
// pins.
func BenchmarkMarshalSmallMap(b *testing.B) {
	c := toml.New()
	in := map[string]int{"a": 1, "b": 2, "c": 3}
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		out, err := c.Marshal(in)
		//: a failure is a broken benchmark.
		if err != nil {
			b.Fatal(err)
		}
		benchSinkBytes = out
	}
}

// BenchmarkUnmarshalLargeMap decodes benchLargeItems tables into
// map[string]any.
func BenchmarkUnmarshalLargeMap(b *testing.B) {
	data := benchLargeDoc()
	c := toml.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	//: the measured loop.
	for b.Loop() {
		out := make(map[string]any)
		//: a failure is a broken benchmark.
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		benchSinkAny = out
	}
}

// BenchmarkUnmarshalLargeStruct decodes benchLargeItems tables into a slice of
// structs.
func BenchmarkUnmarshalLargeStruct(b *testing.B) {
	data := benchLargeDoc()
	c := toml.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	//: the measured loop.
	for b.Loop() {
		var out benchCatalogue
		//: a failure is a broken benchmark.
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		benchSinkAny = out
	}
}

// BenchmarkMarshalLargeStruct encodes benchLargeItems tables from a slice of
// structs.
func BenchmarkMarshalLargeStruct(b *testing.B) {
	var in benchCatalogue
	c := toml.New()
	//: the typed form of the scaling document.
	if err := c.Unmarshal(benchLargeDoc(), &in); err != nil {
		b.Fatalf("seed Unmarshal: %v", err)
	}
	b.ReportAllocs()
	//: the measured loop.
	for b.Loop() {
		out, err := c.Marshal(in)
		//: a failure is a broken benchmark.
		if err != nil {
			b.Fatal(err)
		}
		benchSinkBytes = out
	}
}

// BenchmarkUnmarshalWideTable decodes benchWideKeys keys of one table: the
// cost of finding whether a key is already defined, as the table grows.
func BenchmarkUnmarshalWideTable(b *testing.B) {
	data := benchWideDoc()
	c := toml.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	//: the measured loop.
	for b.Loop() {
		out := make(map[string]any)
		//: a failure is a broken benchmark.
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		benchSinkAny = out
	}
}
