// Package config_test — traced loads, and secret-aware loading (ADR 0097).
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"
)

// originEnvPrefix namespaces every variable these tests set.
const originEnvPrefix string = "KORIGINTEST"

// tracedConf mixes every kind of key an origin can report: defaulted, from a
// file, from the environment, nested, unset, and secret.
type tracedConf struct {
	Port     int              `json:"port"`
	DataDir  string           `json:"data_dir"`
	Token    coresecret.Value `json:"token"`
	Unset    string           `json:"unset"`
	Database struct {
		DSN      coresecret.Value `json:"dsn"`
		MaxConns int              `json:"max_conns"`
	} `json:"database"`
}

// writeJSONFile writes document to a fresh file and returns its path.
func writeJSONFile(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// originIndex keys a traced load's report by key.
func originIndex(origins []coreconfig.OriginValue) map[string]coreconfig.OriginValue {
	index := make(map[string]coreconfig.OriginValue, len(origins))
	for _, origin := range origins {
		index[origin.Key] = origin
	}
	return index
}

// TestLoadSchemaWithOriginsNamesTheLayerAndTheDetail is the provenance claim:
// every leaf key, the layer that supplied its final value, and the variable or
// file behind it — and never the value. It sets environment variables, so it
// does not run in parallel.
func TestLoadSchemaWithOriginsNamesTheLayerAndTheDetail(t *testing.T) {
	file := writeJSONFile(t, `{"data_dir": "/srv/data", "port": 80, "database": {"dsn": "postgres://file-dsn"}}`)
	t.Setenv(originEnvPrefix+"_PORT", "9090")
	t.Setenv(originEnvPrefix+"_TOKEN", "env-token-value")
	schema, err := cfg.NewSchemaValue(cfg.SchemaSpec[tracedConf]{
		Defaults: []coreconfig.DeclaredValue{{Key: "database.max_conns", Value: 16}},
	})
	if err != nil {
		t.Fatalf("NewSchemaValue: %v", err)
	}
	var conf tracedConf
	origins, err := cfg.LoadSchemaWithOrigins(&conf, schema,
		cfg.FileSource("json", file), cfg.EnvSource(originEnvPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaWithOrigins: %v", err)
	}
	want := map[string]coreconfig.OriginValue{
		"data_dir":           {Key: "data_dir", Layer: coreconfig.LayerFile, Detail: file},
		"database.dsn":       {Key: "database.dsn", Layer: coreconfig.LayerFile, Detail: file, Secret: true},
		"database.max_conns": {Key: "database.max_conns", Layer: coreconfig.LayerDefault},
		"port":               {Key: "port", Layer: coreconfig.LayerEnv, Detail: originEnvPrefix + "_PORT"},
		"token":              {Key: "token", Layer: coreconfig.LayerEnv, Detail: originEnvPrefix + "_TOKEN", Secret: true},
		"unset":              {Key: "unset"},
	}
	got := originIndex(origins)
	if len(origins) != len(want) {
		t.Fatalf("%d origins, want %d: %+v", len(origins), len(want), origins)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("origin of %s = %+v, want %+v", key, got[key], expected)
		}
	}
	//: sorted by key, so two loads report identically.
	for index := 1; index < len(origins); index++ {
		if origins[index-1].Key >= origins[index].Key {
			t.Fatalf("origins are not sorted: %q before %q", origins[index-1].Key, origins[index].Key)
		}
	}
	//: the load itself is unchanged.
	if conf.Port != 9090 || conf.Database.MaxConns != 16 || conf.Token.RevealString() != "env-token-value" {
		t.Fatalf("the traced load decoded %+v", conf)
	}
}

// TestLoadWithOriginsNamesAnUndescribedSourceByPosition pins the fallback for
// a Source that does not implement Describer, and that a schemaless traced
// load reports the same vocabulary.
func TestLoadWithOriginsNamesAnUndescribedSourceByPosition(t *testing.T) {
	t.Parallel()
	var conf tracedConf
	origins, err := cfg.LoadWithOrigins(&conf,
		staticSource{m: map[string]any{"port": 1}},
		staticSource{m: map[string]any{"data_dir": "/d"}},
	)
	if err != nil {
		t.Fatalf("LoadWithOrigins: %v", err)
	}
	got := originIndex(origins)
	if got["port"].Layer != coreconfig.LayerSource || got["port"].Detail != "0" {
		t.Errorf("port = %+v, want source #0", got["port"])
	}
	if got["data_dir"].Layer != coreconfig.LayerSource || got["data_dir"].Detail != "1" {
		t.Errorf("data_dir = %+v, want source #1", got["data_dir"])
	}
	if !got["token"].Secret || got["token"].Layer != "" {
		t.Errorf("token = %+v, want an unset secret", got["token"])
	}
}

// TestSecretsFromTheEnvironmentArriveAsWritten pins the secret-aware half: the
// environment's JSON coercion is bypassed for a secret field, so a numeric
// secret is neither refused nor re-spelled — through every entry point.
func TestSecretsFromTheEnvironmentArriveAsWritten(t *testing.T) {
	type tc struct {
		name  string
		value string
	}
	tests := []tc{
		{"an integer", "12345"},
		{"a twenty-three digit token float64 would truncate", "12345678901234567890123"},
		{"an exponent JSON would re-spell", "1e3"},
		{"a boolean", "true"},
		{"JSON null", "null"},
		{"a JSON document", `{"a":1}`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(originEnvPrefix+"_TOKEN", c.value)
		var plain tracedConf
		if err := cfg.Load(&plain, cfg.EnvSource(originEnvPrefix)); err != nil {
			t.Fatalf("%s: Load: %v", c.name, err)
		}
		if plain.Token.RevealString() != c.value {
			t.Fatalf("%s: Load decoded %q, want %q", c.name, plain.Token.RevealString(), c.value)
		}
		var traced tracedConf
		if _, err := cfg.LoadWithOrigins(&traced, cfg.EnvSource(originEnvPrefix)); err != nil || !traced.Token.Equal(plain.Token) {
			t.Fatalf("%s: LoadWithOrigins = (%q, %v)", c.name, traced.Token.RevealString(), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestASecretNeverReachesAnError pins the other half: a secret refused on the
// way in — a number written in a file — fails the load without the number in
// the error, its fields, or its private message.
func TestASecretNeverReachesAnError(t *testing.T) {
	t.Parallel()
	file := writeJSONFile(t, `{"token": 9876543210123}`)
	var conf tracedConf
	_, err := cfg.LoadWithOrigins(&conf, cfg.FileSource("json", file))
	if !errs.HasCode(err, coreconfig.CodeConfigDecodeFailed) {
		t.Fatalf("LoadWithOrigins = %v, want CONFIG_DECODE_FAILED", err)
	}
	var rendered strings.Builder
	rendered.WriteString(err.Error() + errs.PrivateOf(err))
	for _, field := range errs.FieldsOf(err) {
		rendered.WriteString(field.Key() + "=" + field.StringValue())
	}
	if strings.Contains(rendered.String(), "9876543210123") {
		t.Fatalf("the refusal carries the secret: %s", rendered.String())
	}
}

// TestSourcesDescribeThemselves pins the three in-tree describers.
func TestSourcesDescribeThemselves(t *testing.T) {
	t.Setenv(originEnvPrefix+"_NESTED", `{"inner": 1}`)
	env, ok := cfg.EnvSource(originEnvPrefix + "_").(coreconfig.Describer)
	if !ok {
		t.Fatal("EnvSource does not implement Describer")
	}
	if layer, detail := env.Describe("nested.inner"); layer != coreconfig.LayerEnv || detail != originEnvPrefix+"_NESTED" {
		t.Errorf("env Describe(nested.inner) = (%q, %q)", layer, detail)
	}
	if layer, detail := env.Describe("absent"); layer != coreconfig.LayerEnv || detail != originEnvPrefix+"_ABSENT" {
		t.Errorf("env Describe(absent) = (%q, %q), want the variable that would carry it", layer, detail)
	}
	file, ok := cfg.FileSource("json", "/etc/app.json").(coreconfig.Describer)
	if !ok {
		t.Fatal("FileSource does not implement Describer")
	}
	if layer, detail := file.Describe("any.key"); layer != coreconfig.LayerFile || detail != "/etc/app.json" {
		t.Errorf("file Describe = (%q, %q)", layer, detail)
	}
	schema, err := cfg.NewSchemaValue(cfg.SchemaSpec[tracedConf]{})
	if err != nil {
		t.Fatalf("NewSchemaValue: %v", err)
	}
	defaults, ok := schema.Source().(coreconfig.Describer)
	if !ok {
		t.Fatal("the schema's default Source does not implement Describer")
	}
	if layer, detail := defaults.Describe("port"); layer != coreconfig.LayerDefault || detail != "" {
		t.Errorf("default Describe = (%q, %q)", layer, detail)
	}
	var conf tracedConf
	if _, err := cfg.LoadSchemaWithOrigins(&conf, nil); !errs.HasCode(err, coreconfig.CodeConfigSchemaInvalid) {
		t.Errorf("LoadSchemaWithOrigins(nil schema) = %v, want CONFIG_SCHEMA_INVALID", err)
	}
}
