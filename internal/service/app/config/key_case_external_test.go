// Package config_test — later wins whatever case a layer spells a key in.
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/app/config"
	cfg "github.com/kitsunium/sdk/internal/service/app/config"
)

// caseConf is the target the key-case cases decode into: a leaf, a table, and
// a map whose keys are data, not field names.
type caseConf struct {
	Port     int               `json:"port"`
	Database caseDatabase      `json:"database"`
	Labels   map[string]string `json:"labels"`
}

// caseDatabase is caseConf's table.
type caseDatabase struct {
	Host     string `json:"host"`
	MaxConns int    `json:"max_conns"`
}

// layers turns literal maps into sources, in order.
func layers(values ...map[string]any) []coreconfig.Source {
	out := make([]coreconfig.Source, 0, len(values))
	for _, v := range values {
		out = append(out, mapSource{values: v})
	}
	return out
}

// TestLaterWinsWhateverTheCase pins #468's first scenario. encoding/json fills
// one field from "port", "Port" and "PORT" alike; the merge compared them
// exactly, kept both, and the decode's sorted order decided — so the EARLIER
// layer won whenever its spelling sorted last. Every spelling of one field is
// now one key, and the later layer wins it.
func TestLaterWinsWhateverTheCase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		layers []map[string]any
		want   int
	}{
		{"lower then title", []map[string]any{{"port": 1}, {"Port": 2}}, 2},
		{"title then lower", []map[string]any{{"Port": 1}, {"port": 2}}, 2},
		{"upper then mixed", []map[string]any{{"PORT": 1}, {"pOrT": 2}}, 2},
		{"three layers, the middle one shouted", []map[string]any{{"port": 1}, {"PORT": 2}, {"Port": 3}}, 3},
		//: one layer spelling the field twice: the exact spelling wins, as it
		//: does in the decoder, whatever order the map yields.
		{"one layer, exact and variant", []map[string]any{{"port": 1, "Port": 2}}, 1},
		{"one layer, two variants", []map[string]any{{"PORT": 1, "Port": 2}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var conf caseConf
			if err := cfg.Load(&conf, layers(tc.layers...)...); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if conf.Port != tc.want {
				t.Errorf("Port = %d, want %d", conf.Port, tc.want)
			}
		})
	}
}

// TestADefaultNeverBeatsAFileThatSpellsTheKeyDifferently pins #468's second
// scenario: the schema's default is a layer UNDER every source, so a file that
// writes PORT must not lose to a default declared at "port" — with unknown keys
// allowed (where the variant used to slip past the key pass) and in the strict
// schema (where it is the same field, so it is no stranger).
func TestADefaultNeverBeatsAFileThatSpellsTheKeyDifferently(t *testing.T) {
	t.Parallel()
	for _, allowUnknown := range []bool{true, false} {
		schema, err := cfg.NewSchemaValue[caseConf](cfg.SchemaSpec[caseConf]{
			Defaults:         []coreconfig.DeclaredValue{{Key: "port", Value: 80}},
			AllowUnknownKeys: allowUnknown,
		})
		if err != nil {
			t.Fatalf("NewSchema: %v", err)
		}
		var conf caseConf
		if err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{"PORT": 9090}}); err != nil {
			t.Fatalf("AllowUnknownKeys=%v: LoadSchema: %v", allowUnknown, err)
		}
		if conf.Port != 9090 {
			t.Errorf("AllowUnknownKeys=%v: Port = %d, want the file's 9090 over the default 80", allowUnknown, conf.Port)
		}
	}
}

// TestPrecedenceHoldsAcrossSpellings walks default < file < env < override with
// a real file that writes "Port" — the YAML habit — and an environment that
// lowercases, so every layer spells the field its own way.
func TestPrecedenceHoldsAcrossSpellings(t *testing.T) {
	schema, err := cfg.NewSchemaValue[caseConf](cfg.SchemaSpec[caseConf]{
		Defaults: []coreconfig.DeclaredValue{{Key: "port", Value: 8080}},
	})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	path := filepath.Join(t.TempDir(), "app.json")
	if err := os.WriteFile(path, []byte(`{"Port":9090}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	file := cfg.FileSource("json", path)
	//: EnvSource reads the process environment, so this case cannot be parallel.
	t.Setenv("KEYCASETEST_PORT", "7070")
	env := cfg.EnvSource("KEYCASETEST")
	override := mapSource{values: map[string]any{"PORT": 6060}}

	cases := []struct {
		name    string
		sources []coreconfig.Source
		want    int
	}{
		{name: "default only", want: 8080},
		{name: "a file's Port beats the default", sources: []coreconfig.Source{file}, want: 9090},
		{name: "the env's port beats a file's Port", sources: []coreconfig.Source{file, env}, want: 7070},
		{name: "a file's Port after the env beats it", sources: []coreconfig.Source{env, file}, want: 9090},
		{name: "an override's PORT beats them all", sources: []coreconfig.Source{file, env, override}, want: 6060},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var conf caseConf
			if err := cfg.LoadSchema(&conf, schema, tc.sources...); err != nil {
				t.Fatalf("LoadSchema: %v", err)
			}
			if conf.Port != tc.want {
				t.Errorf("Port = %d, want %d", conf.Port, tc.want)
			}
		})
	}
}

// TestATableMergesWhateverTheCase pins the rewrite below the root: a later
// layer's "Database.HOST" overrides an earlier "database.host", and the members
// it does not mention keep the earlier layer's values.
func TestATableMergesWhateverTheCase(t *testing.T) {
	t.Parallel()
	var conf caseConf
	err := cfg.Load(&conf, layers(
		map[string]any{"database": map[string]any{"host": "a", "max_conns": 4}},
		map[string]any{"Database": map[string]any{"HOST": "b"}},
	)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if conf.Database.Host != "b" || conf.Database.MaxConns != 4 {
		t.Errorf("Database = %+v, want host b from the later layer and max_conns 4 kept", conf.Database)
	}
}

// TestAMapKeepsKeysThatDifferByCase pins what is NOT rewritten: the members of
// a map are data, and "Env" and "env" are two of them.
func TestAMapKeepsKeysThatDifferByCase(t *testing.T) {
	t.Parallel()
	var conf caseConf
	err := cfg.Load(&conf, layers(
		map[string]any{"labels": map[string]any{"Env": "a"}},
		map[string]any{"Labels": map[string]any{"env": "b"}},
	)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if conf.Labels["Env"] != "a" || conf.Labels["env"] != "b" || len(conf.Labels) != 2 {
		t.Errorf("Labels = %v, want both Env=a and env=b", conf.Labels)
	}
}

// TestTwoFieldsThatDifferOnlyByCaseStayTwo pins the ambiguous fold: a type
// that tags one field "port" and another "Port" is told apart by the exact
// spelling, and the merge must not fold the two together.
func TestTwoFieldsThatDifferOnlyByCaseStayTwo(t *testing.T) {
	t.Parallel()
	type twins struct {
		Lower int `json:"port"`
		Title int `json:"Port"`
	}
	var conf twins
	if err := cfg.Load(&conf, layers(map[string]any{"port": 1}, map[string]any{"Port": 2})...); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if conf.Lower != 1 || conf.Title != 2 {
		t.Errorf("twins = %+v, want port=1 and Port=2 kept apart", conf)
	}
}

// TestCanonicalisingNeverEditsTheSourcesMap pins that the rewrite works on a
// copy: a source handing back its own map finds it as it was.
func TestCanonicalisingNeverEditsTheSourcesMap(t *testing.T) {
	t.Parallel()
	own := map[string]any{"Port": 2, "Database": map[string]any{"HOST": "b"}}
	var conf caseConf
	if err := cfg.Load(&conf, mapSource{values: own}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, kept := own["Port"]; !kept || len(own) != 2 {
		t.Errorf("the source's map was edited: %v", own)
	}
	if db, _ := own["Database"].(map[string]any); db["HOST"] != "b" || len(db) != 1 {
		t.Errorf("the source's table was edited: %v", own["Database"])
	}
}

// TestAnOriginNamesTheLayerThatWonInAnotherCase pins the traced load: the key
// is reported in the type's spelling, attributed to the later layer that wrote
// it in another case.
func TestAnOriginNamesTheLayerThatWonInAnotherCase(t *testing.T) {
	t.Parallel()
	var conf caseConf
	origins, err := cfg.LoadWithOrigins(&conf, layers(map[string]any{"port": 1}, map[string]any{"PORT": 2})...)
	if err != nil {
		t.Fatalf("LoadWithOrigins: %v", err)
	}
	for _, origin := range origins {
		if origin.Key != "port" {
			continue
		}
		if origin.Layer != coreconfig.LayerSource || origin.Detail != "1" {
			t.Errorf("port's origin = %s/%s, want source/1", origin.Layer, origin.Detail)
		}
		return
	}
	t.Fatal("no origin reported for port")
}
