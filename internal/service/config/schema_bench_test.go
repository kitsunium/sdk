// Package config — what the schema costs on the start-up path.
//
// Loading a configuration is not a hot path: it runs once per process. It is
// measured anyway for two reasons. A Watcher REPLAYS it on every change, so a
// cost that is invisible at boot is not necessarily invisible in production;
// and "compile once, check per load" is a claim, so the two halves are measured
// separately rather than asserted to be different.
package config

import (
	"reflect"
	"strconv"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
)

// typeOfBenchConf is the target type the key walk resolves against.
func typeOfBenchConf() reflect.Type {
	//: one place, so every benchmark walks the same shape.
	return reflect.TypeFor[benchConf]()
}

// benchDatabase is the nested table, so every measurement crosses one level of
// the dotted grammar rather than staying flat.
type benchDatabase struct {
	DSN      string `json:"dsn"`
	Host     string `json:"host"`
	MaxConns int    `json:"max_conns" validate:"min=1,max=512"`
	Timeout  int    `json:"timeout"   validate:"min=0,max=3600"`
}

// benchConf is a realistic service configuration: a dozen keys, one nested
// table, and the `validate` tags a real one carries.
type benchConf struct {
	Port     int           `json:"port"     validate:"min=1,max=65535"`
	Mode     string        `json:"mode"     validate:"oneof=dev|prod"`
	Name     string        `json:"name"     validate:"minlen=1"`
	Replicas int           `json:"replicas" validate:"min=1,max=64"`
	Verbose  bool          `json:"verbose"`
	Database benchDatabase `json:"database" validate:"dive"`
}

// benchSpec is the declaration every case compiles: three defaults, one
// requirement, strict vocabulary.
func benchSpec() SchemaSpec[benchConf] {
	//: the shape a real service declares.
	return SchemaSpec[benchConf]{
		Required: []string{"database.dsn"},
		Defaults: []coreconfig.DeclaredValue{
			{Key: "port", Value: 8080},
			{Key: "mode", Value: "prod"},
			{Key: "replicas", Value: 3},
			{Key: "database.host", Value: "localhost"},
			{Key: "database.max_conns", Value: 16},
			{Key: "database.timeout", Value: 30},
		},
	}
}

// benchLayer is the one source a load reads, standing in for a parsed file.
func benchLayer() mapLayer {
	//: what an operator supplied.
	return mapLayer{values: map[string]any{
		"name":     "checkout",
		"verbose":  true,
		"database": map[string]any{"dsn": "postgres://localhost/app", "max_conns": 32},
	}}
}

// mapLayer is a Source over a literal map — no file, no syscall, so the
// measurement is the loader and not the disk.
type mapLayer struct {
	values map[string]any
}

// Load hands back the literal layer.
func (m mapLayer) Load() (values map[string]any, err error) {
	//: the caller's own map.
	return m.values, nil
}

// BenchmarkNewSchemaValue is the COMPILE half: the reflection walk over the
// target type, the default layer's JSON round trip, the tag plan, and the
// self-contradiction probe. It runs once per process, and it is the price of
// every check below being cheap.
func BenchmarkNewSchemaValue(b *testing.B) {
	spec := benchSpec()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		schema, err := NewSchemaValue[benchConf](spec)
		if err != nil {
			b.Fatalf("NewSchemaValue: %v", err)
		}
		//: keep the result observable so the call is not optimised away.
		if schema == nil {
			b.Fatal("nil schema")
		}
	}
}

// BenchmarkLoadWithoutSchema is the baseline: merge + decode + Validate, the
// contract that existed before the schema. Everything the schema adds is the
// difference between this and the next.
func BenchmarkLoadWithoutSchema(b *testing.B) {
	layer := benchLayer()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var conf benchConf
		if err := Load(&conf, layer); err != nil {
			b.Fatalf("Load: %v", err)
		}
	}
}

// BenchmarkLoadSchema is the whole start-up path with a compiled schema: the
// default layer, the key pass, the decode, and the value constraints.
func BenchmarkLoadSchema(b *testing.B) {
	schema, err := NewSchemaValue[benchConf](benchSpec())
	if err != nil {
		b.Fatalf("NewSchemaValue: %v", err)
	}
	layer := benchLayer()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var conf benchConf
		if loadErr := LoadSchema(&conf, schema, layer); loadErr != nil {
			b.Fatalf("LoadSchema: %v", loadErr)
		}
	}
}

// BenchmarkLoadSchemaAllowUnknown isolates the unknown-key walk by removing it:
// the same load with the check opted out. The difference IS the walk.
func BenchmarkLoadSchemaAllowUnknown(b *testing.B) {
	spec := benchSpec()
	spec.AllowUnknownKeys = true
	schema, err := NewSchemaValue[benchConf](spec)
	if err != nil {
		b.Fatalf("NewSchemaValue: %v", err)
	}
	layer := benchLayer()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var conf benchConf
		if loadErr := LoadSchema(&conf, schema, layer); loadErr != nil {
			b.Fatalf("LoadSchema: %v", loadErr)
		}
	}
}

// BenchmarkCheckKeys is the key pass alone, over an already-merged map: the
// presence lookups and the vocabulary walk, with no decode and no constraint.
func BenchmarkCheckKeys(b *testing.B) {
	schema, err := NewSchemaValue[benchConf](benchSpec())
	if err != nil {
		b.Fatalf("NewSchemaValue: %v", err)
	}
	merged, mergeErr := mergeLayers(schema, []coreconfig.Source{benchLayer()})
	if mergeErr != nil {
		b.Fatalf("mergeLayers: %v", mergeErr)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if keyErr := schema.checkKeys(merged); keyErr != nil {
			b.Fatalf("checkKeys: %v", keyErr)
		}
	}
}

// BenchmarkUnknownKeysWide answers the question a strict vocabulary raises: how
// does the walk scale with a document far wider than the type? Every extra key
// is a map lookup that misses, so the cost is the DOCUMENT's size and not the
// type's.
func BenchmarkUnknownKeysWide(b *testing.B) {
	//: a hundred extra top-level keys, as an unprefixed environment would.
	wide := map[string]any{"port": 8080}
	for index := range 100 {
		wide["extra_"+strconv.Itoa(index)] = index
	}
	known := collectKeys(typeOfBenchConf())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if got := unknownKeys(wide, known, true); len(got) != 100 {
			b.Fatalf("unknown = %d, want 100", len(got))
		}
	}
}

// BenchmarkSchemaSourceLoad measures the defensive copy the default layer hands
// out, since a caller inspecting effective defaults pays it per call.
func BenchmarkSchemaSourceLoad(b *testing.B) {
	schema, err := NewSchemaValue[benchConf](benchSpec())
	if err != nil {
		b.Fatalf("NewSchemaValue: %v", err)
	}
	source := schema.Source()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		values, loadErr := source.Load()
		if loadErr != nil || len(values) == 0 {
			b.Fatalf("Source().Load(): %v", loadErr)
		}
	}
}
