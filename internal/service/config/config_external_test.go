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
		//: every case below decodes as a NUMBER PREFIX unless coercion is gated
		//: on the whole value being one complete JSON document. Each one was
		//: silently truncated: json.Decoder.Decode returns the first token and
		//: reports no error for the trailing bytes.
		{"digit-led hostname keeps its text", "5gc.svc.cluster.local", "5gc.svc.cluster.local"},
		{"duration suffix keeps its text", "1500ms", "1500ms"},
		{"semver keeps its text", "1.2.3", "1.2.3"},
		//: a 5G slice differentiator — hex-ish, digit-led, and business-critical.
		{"hex-ish identifier keeps its text", "0A0A01", "0A0A01"},
		{"trailing letters keep their text", "8080abc", "8080abc"},
		{"CIDR keeps its text", "10.45.0.0/16", "10.45.0.0/16"},
		{"hex literal keeps its text", "0x1F", "0x1F"},
		//: a tracking area code — leading zeros are not JSON numbers at all.
		{"leading-zero identifier keeps its text", "000001", "000001"},
		{"leading-zero number keeps its text", "08", "08"},
		{"date keeps its text", "2026-09-03", "2026-09-03"},
		//: two tokens are not one document, separated by space or comma.
		{"two tokens keep their text", "1 2", "1 2"},
		{"comma-separated keeps its text", "1,2", "1,2"},
		{"trailing dot keeps its text", "1.", "1."},
		{"leading dot keeps its text", ".5", ".5"},
		//: guards against over-correcting: these must STILL coerce. The three
		//: whitespace rows are the ones that rule out trimming BOTH ends before
		//: the comparison — a leading blank is consumed by the decoder, so
		//: removing it from the denominator would turn " 8080" back into text.
		{"leading whitespace still coerces", " 8080", int64(8080)},
		{"trailing whitespace still coerces", "8080 ", int64(8080)},
		{"tabs and newlines still coerce", "\t8080\n", int64(8080)},
		//: guards against over-correcting: these must STILL coerce.
		{"surrounding whitespace still coerces", " 8080 ", int64(8080)},
		{"exponent still coerces", "1e9", 1e9},
		{"dotted hostname stays a string as before", "sdm.halys.fr", "sdm.halys.fr"},
		{"leading-plus stays a string as before", "+33", "+33"},
		{"empty stays empty", "", ""},
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

// sliceConf mirrors a real 5G-core config: identifiers that look numeric but are
// text, and one genuinely numeric field.
type sliceConf struct {
	SD      string `json:"sd"`
	Host    string `json:"host"`
	Timeout string `json:"timeout"`
	Retries int    `json:"retries"`
}

// TestEnvTruncationEndToEnd is the regression guard for the env-coercion
// truncation, asserted where it actually hurts: the value that lands in the
// caller's struct.
//
// The map-level test pins the coercion contract; this one pins the consequence.
// A truncated value has two ways to reach the operator, and both were silent
// about the cause: into a string field it aborted the WHOLE load with a decode
// error naming no variable, and into a numeric field it simply arrived wrong —
// SD=0A0A01 becoming 0, TIMEOUT=1500ms becoming 1500. The second is the worse
// one, because the configuration looks accepted.
func TestEnvTruncationEndToEnd(t *testing.T) {
	//: a realistic 5G-core environment, set in one loop.
	for k, v := range map[string]string{
		"SLICE_SD":      "0A0A01",
		"SLICE_HOST":    "5gc.svc.cluster.local",
		"SLICE_TIMEOUT": "1500ms",
		"SLICE_RETRIES": "3",
	} {
		t.Setenv(k, v)
	}
	var got sliceConf
	//: the whole load must succeed — a truncated string field used to abort it.
	if err := cfg.Load(&got, cfg.EnvSource("SLICE")); err != nil {
		t.Fatalf("Load: %v, want nil", err)
	}
	type tc struct {
		name string
		got  any
		want any
	}
	tests := []tc{
		{"slice differentiator survives", got.SD, "0A0A01"},
		{"service name survives", got.Host, "5gc.svc.cluster.local"},
		{"duration survives", got.Timeout, "1500ms"},
		//: the genuinely numeric field must still coerce.
		{"real integer still coerces", got.Retries, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			//: each field carries exactly what the operator set.
			if tc.got != tc.want {
				t.Errorf("got %v (%T), want %v (%T)", tc.got, tc.got, tc.want, tc.want)
			}
		})
	}
}

// TestEnvTruncationIntoNumericField pins the silent arm specifically: when the
// target field is numeric, a truncated value does not fail the load at all — it
// arrives as a plausible number the operator never wrote.
func TestEnvTruncationIntoNumericField(t *testing.T) {
	type numConf struct {
		Timeout int `json:"timeout"`
	}
	type tc struct {
		name    string
		env     string
		wantErr bool
		want    int
	}
	tests := []tc{
		//: the defect case — "1500ms" must NOT quietly become the integer 1500.
		{"duration string is not a number", "1500ms", true, 0},
		//: a real integer still decodes into the numeric field.
		{"plain integer still decodes", "1500", false, 1500},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		t.Setenv("NUM_TIMEOUT", tc.env)
		var got numConf
		err := cfg.Load(&got, cfg.EnvSource("NUM"))
		//: a non-numeric value must be REFUSED by the typed decode, not silently
		//: truncated into the field.
		if (err != nil) != tc.wantErr {
			t.Fatalf("Load err=%v, wantErr=%v", err, tc.wantErr)
		}
		//: a refused load must leave the field untouched.
		if got.Timeout != tc.want {
			t.Errorf("Timeout=%d, want %d", got.Timeout, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestEnvSourcePrefixSpelling pins that a trailing underscore on the prefix is
// absorbed rather than doubled.
//
// EnvSource supplies the separator itself, so EnvSource("APP_") used to search
// for "APP__" — which matches nothing. Load then returned an empty struct and a
// NIL ERROR: the caller got no configuration and no signal, which reads exactly
// like "the SDK is broken" rather than "the prefix has one character too many".
// Both spellings are natural to write, so both name the same namespace.
func TestEnvSourcePrefixSpelling(t *testing.T) {
	type tc struct {
		name   string
		prefix string
	}
	tests := []tc{
		{"prefix without the separator", "PFX"},
		{"prefix with a trailing separator", "PFX_"},
		{"prefix with several trailing separators", "PFX___"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		t.Setenv("PFX_HOST", "sdm.halys.fr")
		t.Setenv("PFX_PORT", "8000")
		var got appConf
		//: whichever spelling the caller used, the same variables must load.
		if err := cfg.Load(&got, cfg.EnvSource(tc.prefix)); err != nil {
			t.Fatalf("Load: %v, want nil", err)
		}
		want := appConf{Port: 8000, Host: "sdm.halys.fr"}
		//: the observable contract is the populated struct, not the match string.
		if got != want {
			t.Errorf("Load = %+v, want %+v", got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestEnvSourceEmptyPrefixStillReadsEverything guards the other arm: absorbing
// a trailing underscore must not turn an all-variables source into a prefixed
// one, since "" and "_" are different intents.
func TestEnvSourceEmptyPrefixStillReadsEverything(t *testing.T) {
	type tc struct {
		name string
		key  string
		want any
	}
	tests := []tc{
		//: an empty prefix reads the whole environment, key lower-cased as-is.
		{"unprefixed variable is read", "unprefixed_marker", "present"},
		//: and the underscore is part of the key, not a stripped separator.
		{"prefixed variable keeps its full key", "pfx_marker", "also-present"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		t.Setenv("UNPREFIXED_MARKER", "present")
		t.Setenv("PFX_MARKER", "also-present")
		m, err := cfg.EnvSource("").Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if m[tc.key] != tc.want {
			t.Errorf("%s=%v, want %v", tc.key, m[tc.key], tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
