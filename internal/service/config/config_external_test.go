package config_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
)

type appConf struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

// Validate implements core/config.Validator.
func (c appConf) Validate() error {
	//: a zero port is invalid.
	if c.Port == 0 {
		//: surface a plain error; Load relabels it CONFIG_VALIDATION_FAILED.
		return errs.NewRuntime(coreconfig.CodeConfigValidationFailed, "PORT_REQUIRED", "port required", "test: port is zero")
	}
	//: otherwise valid.
	return nil
}

// TestEnvSource reads prefixed env vars into a flat map.
func TestEnvSource(t *testing.T) {
	t.Setenv("APP_PORT", "8080")
	t.Setenv("OTHER_X", "ignored")
	m, err := cfg.EnvSource("APP").Load()
	//: the load never errors.
	if err != nil {
		t.Fatalf("EnvSource.Load: %v", err)
	}
	//: the prefixed key is stripped, lower-cased, and coerced to an INTEGER.
	//: asserting float64 here would have masked the coercion defect: a plain
	//: json.Unmarshal into an any widens every numeric token to float64.
	if got, ok := m["port"].(int64); !ok || got != 8080 {
		t.Errorf("port=%v (%T), want int64(8080)", m["port"], m["port"])
	}
	//: a non-prefixed var is excluded.
	if _, ok := m["x"]; ok {
		t.Error("OTHER_X leaked into the APP namespace")
	}
}

// TestLoadMergeValidate merges file + env (env wins) and validates.
func TestLoadMergeValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	//: the file sets host + a port that env will override.
	if err := os.WriteFile(path, []byte(`{"port":1,"host":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PORT", "9090")
	var conf appConf
	//: env is listed after the file, so its port wins.
	err := cfg.Load(&conf, cfg.FileSource("json", path), cfg.EnvSource("APP"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	//: env overrode the file port.
	if conf.Port != 9090 {
		t.Errorf("Port=%d, want 9090 (env override)", conf.Port)
	}
	//: the file-only field survived.
	if conf.Host != "file" {
		t.Errorf("Host=%q, want file", conf.Host)
	}
}

// TestValidationFailure surfaces CONFIG_VALIDATION_FAILED.
func TestValidationFailure(t *testing.T) {
	t.Parallel()
	var conf appConf // Port stays 0 → Validate fails
	err := cfg.Load(&conf, cfg.EnvSource("MISSING"))
	//: a zero-port config fails validation with the typed code.
	if !errs.HasCode(err, coreconfig.CodeConfigValidationFailed) {
		t.Errorf("err=%v, want CONFIG_VALIDATION_FAILED", err)
	}
}

// TestPollWatcher detects a file change and fires onChange.
func TestPollWatcher(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "watched.json")
	//: seed the file so the initial fingerprint succeeds.
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := cfg.PollWatcher(path, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	var watchErr error
	//: Watch blocks until cancel; wg.Wait joins it at the end.
	wg.Go(func() {
		watchErr = w.Watch(ctx, func() {
			//: non-blocking signal so the watcher never stalls.
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	})
	//: mutate the file so mtime/size changes.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"v":222}`), 0o600); err != nil {
		t.Fatal(err)
	}
	//: expect a change notification within a generous window.
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("poll watcher did not detect the change")
	}
	cancel()
	wg.Wait()
	//: a clean ctx-cancel stop returns nil.
	if watchErr != nil {
		t.Errorf("Watch returned %v, want nil on cancel", watchErr)
	}
}

// TestEnvCoercionKeepsIntegers pins the documented coercion contract: numeric
// env values become integers, not float64. A bare json.Unmarshal into an `any`
// widens every numeric token to float64, which loses integrality and, past
// 2^53, exactness.
func TestEnvCoercionKeepsIntegers(t *testing.T) {
	type tc struct {
		name string
		env  string
		want any
	}
	tests := []tc{
		{"small-int", "8080", int64(8080)},
		{"zero", "0", int64(0)},
		{"negative", "-17", int64(-17)},
		//: past 2^53 a float64 round-trip would silently change the value.
		{"beyond-float64-exactness", "9007199254740993", int64(9007199254740993)},
		{"genuine-fraction", "1.5", 1.5},
		{"bool", "true", true},
		{"bare-word-stays-string", "kitsune", "kitsune"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		t.Setenv("COERCE_V", tc.env)
		m, err := cfg.EnvSource("COERCE").Load()
		if err != nil {
			t.Fatalf("%s: Load: %v", tc.name, err)
		}
		//: both the value AND its dynamic type are part of the contract.
		if m["v"] != tc.want {
			t.Errorf("%s: v=%v (%T), want %v (%T)", tc.name, m["v"], m["v"], tc.want, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestWatchRejectsBadInput pins the input guards on the poll watcher. Both
// paths previously crashed the process: time.NewTicker panics on a non-positive
// interval, and a nil onChange panicked on the first detected change.
func TestWatchRejectsBadInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	type tc struct {
		name     string
		interval time.Duration
		onChange func()
	}
	tests := []tc{
		{"zero-interval", 0, func() {}},
		{"negative-interval", -time.Second, func() {}},
		{"nil-callback", time.Second, nil},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: a bad input must return, not panic.
		err := cfg.PollWatcher(path, tc.interval).Watch(t.Context(), tc.onChange)
		//: and it must be the documented watch sentinel.
		if !errs.HasCode(err, coreconfig.CodeConfigWatchFailed) {
			t.Errorf("%s: err=%v, want CodeConfigWatchFailed", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestMergeDoesNotAliasSourceMaps pins layer isolation: merging must never
// store a source-owned nested map by reference, because a later layer merging
// the same key would then mutate the earlier source's own map.
func TestMergeDoesNotAliasSourceMaps(t *testing.T) {
	t.Parallel()
	//: layer one owns a nested map it hands to the loader.
	first := map[string]any{"db": map[string]any{"host": "a"}}
	second := map[string]any{"db": map[string]any{"port": int64(5432)}}
	var target struct {
		DB struct {
			Host string `json:"host"`
			Port int64  `json:"port"`
		} `json:"db"`
	}
	if err := cfg.Load(&target, staticSource{first}, staticSource{second}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	//: the merged result carries both layers.
	if target.DB.Host != "a" || target.DB.Port != 5432 {
		t.Errorf("merged=%+v, want host=a port=5432", target.DB)
	}
	//: and the FIRST source's map is untouched — no key leaked back into it.
	inner, _ := first["db"].(map[string]any)
	if _, leaked := inner["port"]; leaked {
		t.Error("merging layer 2 mutated layer 1's own map — source aliasing")
	}
}

// staticSource is an in-test core/config.Source returning a fixed layer.
type staticSource struct{ m map[string]any }

func (s staticSource) Load() (map[string]any, error) { return s.m, nil }
