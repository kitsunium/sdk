// Package config_test — the file Source over an io/fs.FS, as a program reading
// its own embedded configuration configures it.
package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
	_ "github.com/kitsunium/sdk/internal/service/codec/yaml" // register "yaml"
)

// fsEnvPrefix namespaces every variable these tests set.
const fsEnvPrefix string = "KFSSOURCETEST"

// embedded is the tree a program would carry with //go:embed config.
func embedded() fstest.MapFS {
	return fstest.MapFS{
		"config/config.yaml":     {Data: []byte("port: 8080\nhost: kitsune\ndatabase:\n  max_conns: 16\n")},
		"config/production.json": {Data: []byte(`{"host": "prod.internal", "database": {"max_conns": 64}}`)},
		"config/junk.json":       {Data: []byte("this is not json at all\n")},
		"config/empty.json":      {Data: nil},
	}
}

// TestFSSource pins that an embedded file loads like a file on disk and that
// every way of getting one wrong is the ONE typed failure FileSource returns —
// a missing file included, which is a refusal and not an empty layer.
func TestFSSource(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fsys    fstest.MapFS
		format  codec.Format
		path    string
		want    map[string]any
		wantErr bool
	}
	tests := []tc{
		{
			name: "a JSON document", fsys: embedded(), format: "json", path: "config/production.json",
			want: map[string]any{"host": "prod.internal", "database": map[string]any{"max_conns": float64(64)}},
		},
		{
			name: "a YAML document", fsys: embedded(), format: "yaml", path: "config/config.yaml",
			want: map[string]any{"port": 8080, "host": "kitsune", "database": map[string]any{"max_conns": 16}},
		},
		{name: "a file the filesystem does not hold", fsys: embedded(), format: "json", path: "config/staging.json", wantErr: true},
		{name: "a directory where a file belongs", fsys: embedded(), format: "json", path: "config", wantErr: true},
		{name: "a name that climbs out of the tree", fsys: embedded(), format: "json", path: "../config/production.json", wantErr: true},
		{name: "a rooted name", fsys: embedded(), format: "json", path: "/config/production.json", wantErr: true},
		{name: "a file that is not the declared format", fsys: embedded(), format: "json", path: "config/junk.json", wantErr: true},
		{name: "an empty file", fsys: embedded(), format: "json", path: "config/empty.json", wantErr: true},
		{name: "a format nobody registered", fsys: embedded(), format: "yaml-not-imported", path: "config/config.yaml", wantErr: true},
		{name: "no filesystem at all", fsys: nil, format: "json", path: "config/production.json", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a nil MapFS would be a non-nil fs.FS holding a nil map; the nil
		//: case is the interface itself being nil.
		var source coreconfig.Source
		if c.fsys == nil {
			source = cfg.FSSource(nil, c.format, c.path)
		} else {
			source = cfg.FSSource(c.fsys, c.format, c.path)
		}
		got, err := source.Load()

		if c.wantErr {
			//: one code, because the caller's two answers — drop the layer or
			//: refuse to start — do not depend on which way the file failed.
			if !errs.HasCode(err, coreconfig.CodeConfigSourceFailed) {
				t.Fatalf("Load(%s) = %v, want CONFIG_SOURCE_FAILED", c.name, err)
			}
			//: a refused layer hands back nothing for the loader to merge.
			if got != nil {
				t.Errorf("Load(%s) returned %v beside the error", c.name, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Load(%s) = %v, want nil", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Load(%s) = %#v, want %#v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFSSourceNamesWhatIsMissing pins the two refusals whose cause is not the
// file: the error carries the format nobody registered, or says there was no
// filesystem — so the two are told apart in a log without reading code.
func TestFSSourceNamesWhatIsMissing(t *testing.T) {
	t.Parallel()
	field := func(err error, key string) string {
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == key {
				return f.StringValue()
			}
		}
		return ""
	}
	_, unregistered := cfg.FSSource(embedded(), "nope-not-registered", "config/config.yaml").Load()
	if got := field(unregistered, "format"); got != "nope-not-registered" {
		t.Errorf("the unregistered format is annotated %q, want the format", got)
	}
	_, nothing := cfg.FSSource(nil, "json", "config/production.json").Load()
	if got := field(nothing, "fs"); got != "nil" {
		t.Errorf("the missing filesystem is annotated %q, want \"nil\"", got)
	}
}

// TestFSSourceAnswersLikeFileSource pins the equivalence the source is built
// on: the same bytes, read from disk by FileSource or through os.DirFS by
// FSSource, are the same layer, and an absent file is the same refusal.
func TestFSSourceAnswersLikeFileSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.json"), []byte(`{"port": 8080, "nested": {"on": true}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fromDisk, diskErr := cfg.FileSource("json", filepath.Join(dir, "app.json")).Load()
	fromFS, fsErr := cfg.FSSource(os.DirFS(dir), "json", "app.json").Load()
	if diskErr != nil || fsErr != nil {
		t.Fatalf("Load = %v / %v, want nil / nil", diskErr, fsErr)
	}
	if !reflect.DeepEqual(fromDisk, fromFS) {
		t.Errorf("FileSource read %#v, FSSource read %#v", fromDisk, fromFS)
	}
	_, diskAbsent := cfg.FileSource("json", filepath.Join(dir, "absent.json")).Load()
	_, fsAbsent := cfg.FSSource(os.DirFS(dir), "json", "absent.json").Load()
	if !errs.HasCode(diskAbsent, coreconfig.CodeConfigSourceFailed) || !errs.HasCode(fsAbsent, coreconfig.CodeConfigSourceFailed) {
		t.Errorf("an absent file = %v / %v, want CONFIG_SOURCE_FAILED from both", diskAbsent, fsAbsent)
	}
}

// fsConf is the configuration an embedded tree and the environment fill
// together.
type fsConf struct {
	Port     int    `json:"port"`
	Host     string `json:"host"`
	Database struct {
		MaxConns int `json:"max_conns"`
	} `json:"database"`
}

// TestFSSourceLayeredUnderTheEnvironment is the use the source exists for: a
// program's committed configuration, then the one for its environment, both
// read from the binary, under the process environment — and a traced load
// reports each embedded document as a file, with the path as given. It sets
// an environment variable, so it does not run in parallel.
func TestFSSourceLayeredUnderTheEnvironment(t *testing.T) {
	t.Setenv(fsEnvPrefix+"_PORT", "9090")
	files := embedded()
	var conf fsConf
	origins, err := cfg.LoadWithOrigins(&conf,
		cfg.FSSource(files, "yaml", "config/config.yaml"),
		cfg.FSSource(files, "json", "config/production.json"),
		cfg.EnvSource(fsEnvPrefix),
	)
	if err != nil {
		t.Fatalf("LoadWithOrigins: %v", err)
	}
	if conf.Port != 9090 || conf.Host != "prod.internal" || conf.Database.MaxConns != 64 {
		t.Fatalf("the layered load decoded %+v", conf)
	}
	want := map[string]coreconfig.OriginValue{
		"database.max_conns": {Key: "database.max_conns", Layer: coreconfig.LayerFile, Detail: "config/production.json"},
		"host":               {Key: "host", Layer: coreconfig.LayerFile, Detail: "config/production.json"},
		"port":               {Key: "port", Layer: coreconfig.LayerEnv, Detail: fsEnvPrefix + "_PORT"},
	}
	if len(origins) != len(want) {
		t.Fatalf("%d origins, want %d: %+v", len(origins), len(want), origins)
	}
	for _, origin := range origins {
		if origin != want[origin.Key] {
			t.Errorf("origin of %s = %+v, want %+v", origin.Key, origin, want[origin.Key])
		}
	}
	//: the committed document alone attributes its keys to its own path.
	var base fsConf
	baseOrigins, err := cfg.LoadWithOrigins(&base, cfg.FSSource(files, "yaml", "config/config.yaml"))
	if err != nil {
		t.Fatalf("LoadWithOrigins(config.yaml): %v", err)
	}
	for _, origin := range baseOrigins {
		if origin.Layer != coreconfig.LayerFile || origin.Detail != "config/config.yaml" {
			t.Errorf("origin of %s = %+v, want the file config/config.yaml", origin.Key, origin)
		}
	}
	if base.Port != 8080 || base.Host != "kitsune" || base.Database.MaxConns != 16 {
		t.Errorf("config.yaml alone decoded %+v", base)
	}
}

// permissiveFS is an fs.ReadFileFS that answers every name it is given,
// "../escape.json" included, and counts how often it was asked. fs.ReadFile
// hands a name straight to ReadFile, so it is what an implementation that
// resolved a climbing name would look like.
type permissiveFS struct {
	reads *atomic.Int64
}

// Open is never used: fs.ReadFile prefers ReadFile.
func (permissiveFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// ReadFile returns a valid document for any name, and counts the call.
func (p permissiveFS) ReadFile(string) ([]byte, error) {
	p.reads.Add(1)
	return []byte(`{"port": 1}`), nil
}

// TestFSSourceRefusesANameBeforeAskingTheFilesystem pins that the name rule is
// the source's own and not the filesystem's: a name outside fs.ValidPath is
// refused before anything is read, even from a filesystem that would answer
// it.
func TestFSSourceRefusesANameBeforeAskingTheFilesystem(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
	}
	tests := []tc{
		{name: "a name that climbs out", path: "../escape.json"},
		{name: "a rooted name", path: "/etc/app.json"},
		{name: "an empty name", path: ""},
		{name: "a name with a dot element", path: "config/./app.json"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reads := &atomic.Int64{}
		got, err := cfg.FSSource(permissiveFS{reads: reads}, "json", c.path).Load()
		if !errs.HasCode(err, coreconfig.CodeConfigSourceFailed) || got != nil {
			t.Fatalf("Load(%s) = %v, %v; want CONFIG_SOURCE_FAILED and nothing", c.name, got, err)
		}
		if n := reads.Load(); n != 0 {
			t.Errorf("Load(%s) asked the filesystem %d time(s) for a name it refuses", c.name, n)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the control: a valid name IS read from the same filesystem.
	reads := &atomic.Int64{}
	if _, err := cfg.FSSource(permissiveFS{reads: reads}, "json", "config/app.json").Load(); err != nil || reads.Load() != 1 {
		t.Fatalf("a valid name = %v after %d read(s), want nil after 1", err, reads.Load())
	}
}
