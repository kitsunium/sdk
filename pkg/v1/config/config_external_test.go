package config_test

import (
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/pkg/v1/config"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/secret"

	_ "github.com/kitsunium/sdk/pkg/v1/codec" // register the formats the file sources read
)

type conf struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// The facade is a thin re-export, so what needs pinning is that an env layer
// reaches the typed struct through it: the prefix is stripped, the field name
// is matched through the json tag, and an absent variable leaves the zero
// value rather than erroring — a config loader that refused every unset
// optional would be unusable.
func TestFacade(t *testing.T) {
	// No t.Parallel: t.Setenv mutates a process-wide variable.
	type tc struct {
		name     string
		env      map[string]string
		wantName string
		wantPort int
	}
	tests := []tc{
		{"a single field", map[string]string{"SVC_NAME": "kitsune"}, "kitsune", 0},
		{
			"several fields decode by type",
			map[string]string{"SVC_NAME": "kitsune", "SVC_PORT": "8080"},
			"kitsune", 8080,
		},
		{"an absent variable leaves the zero value", nil, "", 0},
		{
			// The prefix is what scopes the layer; a variable outside it is
			// none of this config's business.
			"a variable outside the prefix is ignored",
			map[string]string{"OTHER_NAME": "nope"},
			"", 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		var got conf
		if err := config.Load(&got, config.EnvSource("SVC")); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Name != c.wantName {
			t.Errorf("Name = %q, want %q", got.Name, c.wantName)
		}
		if got.Port != c.wantPort {
			t.Errorf("Port = %d, want %d", got.Port, c.wantPort)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// schemaConf is the target the schema cases decode into. The `validate` tag is
// the validation domain's, unchanged — the facade re-exports one engine, it
// does not wrap a second.
type schemaConf struct {
	Name string `json:"name" validate:"minlen=1"`
	Port int    `json:"port" validate:"min=1,max=65535"`
}

// TestFacadeSchema pins that the schema reaches a consumer through the public
// package: the three answers ADR 0061 exists to give — a required key that was
// not supplied, a key nothing reads, and a default that fills an absent key
// without touching an explicit zero — are all reachable from pkg/v1/config
// alone, with the sentinels matchable by the names it exports.
func TestFacadeSchema(t *testing.T) {
	t.Parallel()
	schema, err := config.NewSchema[schemaConf](config.SchemaSpec[schemaConf]{
		Required: []string{"name"},
		Defaults: []config.Default{{Key: "port", Value: 8080}},
	})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	//: the facade exports sentinels, not codes, so each expected code is read
	//: back off its own sentinel — which also pins that they are distinct.
	codeOf := func(sentinel error) errs.Code {
		code, ok := errs.CodeOf(sentinel)
		if !ok {
			t.Fatalf("sentinel %v carries no code", sentinel)
		}
		return code
	}

	//: the default fills the absent key; the required one is supplied.
	var filled schemaConf
	if loadErr := config.LoadSchema(&filled, schema, mapLayer{"name": "kitsune"}); loadErr != nil {
		t.Fatalf("LoadSchema: %v", loadErr)
	}
	if filled.Port != 8080 {
		t.Errorf("Port = %d, want the default 8080", filled.Port)
	}

	//: the required key is missing — at start-up, not at first access.
	var missing schemaConf
	missingErr := config.LoadSchema(&missing, schema, mapLayer{"port": 9090})
	if !errs.HasCode(missingErr, codeOf(config.KeyMissing)) {
		t.Errorf("missing-key error = %v, want KeyMissing", missingErr)
	}

	//: a key nothing reads is refused by default.
	var typo schemaConf
	typoErr := config.LoadSchema(&typo, schema, mapLayer{"name": "kitsune", "namme": "x"})
	if !errs.HasCode(typoErr, codeOf(config.UnknownKey)) {
		t.Errorf("unknown-key error = %v, want UnknownKey", typoErr)
	}

	//: a key both required and defaulted cannot be built at all.
	_, contradiction := config.NewSchema[schemaConf](config.SchemaSpec[schemaConf]{
		Required: []string{"port"},
		Defaults: []config.Default{{Key: "port", Value: 8080}},
	})
	if !errs.HasCode(contradiction, codeOf(config.SchemaInvalid)) {
		t.Errorf("contradiction error = %v, want SchemaInvalid", contradiction)
	}
}

// mapLayer is a Source over a literal map, so the schema cases need no file and
// no environment.
type mapLayer map[string]any

// Load hands the literal layer back.
func (m mapLayer) Load() (values map[string]any, err error) {
	//: the caller's own map.
	return m, nil
}

// secretConf is a consumer's configuration with a secret beside a port.
type secretConf struct {
	Port  int          `json:"port"`
	Token secret.Value `json:"token"`
}

// TestOriginsThroughTheFacade pins the traced load through public names: the
// layer and the variable behind each key, the secret marked, the numeric
// secret decoded exactly as written, and no value anywhere in the report.
func TestOriginsThroughTheFacade(t *testing.T) {
	// No t.Parallel: t.Setenv mutates a process-wide variable.
	t.Setenv("FACADEORIGIN_PORT", "8080")
	t.Setenv("FACADEORIGIN_TOKEN", "12345678901234567890123")
	var c secretConf
	origins, err := config.LoadWithOrigins(&c, config.EnvSource("FACADEORIGIN"))
	if err != nil {
		t.Fatalf("LoadWithOrigins: %v", err)
	}
	if c.Port != 8080 || c.Token.RevealString() != "12345678901234567890123" {
		t.Fatalf("decoded port %d and a token of %d bytes", c.Port, c.Token.Len())
	}
	want := []config.Origin{
		{Key: "port", Layer: config.LayerEnv, Detail: "FACADEORIGIN_PORT"},
		{Key: "token", Layer: config.LayerEnv, Detail: "FACADEORIGIN_TOKEN", Secret: true},
	}
	if len(origins) != len(want) {
		t.Fatalf("origins = %+v, want %+v", origins, want)
	}
	for index := range want {
		if origins[index] != want[index] {
			t.Errorf("origins[%d] = %+v, want %+v", index, origins[index], want[index])
		}
	}
	describer, ok := config.FileSource("json", "/etc/app.json").(config.Describer)
	if !ok {
		t.Fatal("FileSource does not implement Describer")
	}
	if layer, detail := describer.Describe("port"); layer != config.LayerFile || detail != "/etc/app.json" {
		t.Errorf("FileSource Describe = (%q, %q)", layer, detail)
	}
	if config.LayerDefault != "default" || config.LayerSource != "source" {
		t.Error("the layer names drifted from the documented strings")
	}
}

// TestFSSourceThroughTheFacade pins the embedded-configuration path through
// public names: a document read from an fs.FS layers under the environment,
// the traced load reports it as a file with the path as given, and a file the
// filesystem does not hold is SourceFailed rather than an empty layer.
func TestFSSourceThroughTheFacade(t *testing.T) {
	// No t.Parallel: t.Setenv mutates a process-wide variable.
	t.Setenv("FACADEFS_PORT", "9090")
	files := fstest.MapFS{
		"config/config.yaml": {Data: []byte("name: kitsune\nport: 8080\n")},
	}
	var c conf
	origins, err := config.LoadWithOrigins(&c,
		config.FSSource(files, "yaml", "config/config.yaml"),
		config.EnvSource("FACADEFS"),
	)
	if err != nil {
		t.Fatalf("LoadWithOrigins: %v", err)
	}
	if c.Name != "kitsune" || c.Port != 9090 {
		t.Fatalf("decoded %+v, want the file's name and the environment's port", c)
	}
	want := []config.Origin{
		{Key: "name", Layer: config.LayerFile, Detail: "config/config.yaml"},
		{Key: "port", Layer: config.LayerEnv, Detail: "FACADEFS_PORT"},
	}
	if len(origins) != len(want) {
		t.Fatalf("origins = %+v, want %+v", origins, want)
	}
	for index := range want {
		if origins[index] != want[index] {
			t.Errorf("origins[%d] = %+v, want %+v", index, origins[index], want[index])
		}
	}
	code, ok := errs.CodeOf(config.SourceFailed)
	if !ok {
		t.Fatal("SourceFailed carries no code")
	}
	var absent conf
	if err := config.Load(&absent, config.FSSource(files, "yaml", "config/production.yaml")); !errs.HasCode(err, code) {
		t.Errorf("an absent embedded file = %v, want SourceFailed", err)
	}
}
