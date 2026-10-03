// Package config — white-box tests for the environment Source. The two helpers
// below decide what a caller's prefix means and what a variable's text becomes,
// and both have failure modes that are silent rather than loud.
package config

import (
	"os"
	"testing"
)

// Test_matchPrefix pins the namespace resolution.
//
// The all-underscore case is the trap. A bare TrimRight leaves "", and "" is
// the read-everything mode — so a prefix of "_" would quietly widen from the
// leading-underscore namespace to the entire process environment, picking up
// PATH, HOME and every secret the deployment happens to export.
func Test_matchPrefix(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"no prefix reads everything", "", ""},
		{"a bare name", "APP", "APP_"},
		{"a name with one trailing separator", "APP_", "APP_"},
		{"a name with several trailing separators", "APP___", "APP_"},
		{"a single underscore is the underscore namespace", "_", "_"},
		{"several underscores are still the underscore namespace", "___", "_"},
		{"a name with an interior underscore", "MY_APP", "MY_APP_"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := matchPrefix(c.in)
		if got != c.want {
			t.Errorf("matchPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
		//: a non-empty prefix must NEVER resolve to the read-everything mode.
		if c.in != "" && got == "" {
			t.Errorf("matchPrefix(%q) collapsed to the whole environment", c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_coerceEnvInto pins the typing rules, and the gate that keeps them from
// silently truncating.
//
// json.Decoder.Decode stops at the end of the first token and reports nothing
// about whatever trails it. Without the whole-document gate, every value that
// merely STARTS like a number is cut down to that prefix: "0A0A01" becomes 0,
// "1500ms" becomes 1500, "10.45.0.0/16" becomes 10.45. None of those is a parse
// failure a caller can see — each is a plausible-looking value of the wrong
// type, which either lands in the target silently or fails the whole Load with
// a decode error naming nothing.
//
// The integer half matters for the same reason: decoding into a bare any widens
// every number to float64, so 8080 becomes 8080.0 and any identifier above 2^53
// loses its exact value.
func Test_coerceEnvInto(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		val  string
		want any
	}
	tests := []tc{
		{"an integer", "8080", int64(8080)},
		{"a negative integer", "-1", int64(-1)},
		{"an integer past 2^53", "9007199254740993", int64(9007199254740993)},
		{"a genuine fraction", "1.5", 1.5},
		{"a boolean", "true", true},
		{"a quoted string", `"kitsune"`, "kitsune"},
		{"a bare word", "kitsune", "kitsune"},
		{"an empty value", "", ""},
		{"surrounding whitespace still coerces", " 8080 ", int64(8080)},
		//: everything below merely STARTS like a number.
		{"a hex-looking identifier", "0A0A01", "0A0A01"},
		{"a duration", "1500ms", "1500ms"},
		{"a hostname starting with a digit", "5gc.svc.cluster.local", "5gc.svc.cluster.local"},
		{"a CIDR block", "10.45.0.0/16", "10.45.0.0/16"},
		{"a version string", "1.2.3", "1.2.3"},
		{"a number with trailing text", "8080x", "8080x"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		out := make(map[string]any, 1)

		coerceEnvInto(out, "k", c.val)

		got, present := out["k"]
		if !present {
			t.Fatalf("coerceEnvInto(%q) stored nothing", c.val)
		}
		if got != c.want {
			t.Errorf("coerceEnvInto(%q) = %#v (%T), want %#v (%T)", c.val, got, got, c.want, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_envSource_Load pins the scan itself: the prefix is stripped, the key is
// lower-cased, and a malformed environ entry is skipped rather than stored under
// a nonsense key.
func Test_envSource_Load(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name    string
		prefix  string
		env     map[string]string
		wantKey string
		wantVal any
	}
	tests := []tc{
		{"a prefixed integer", "APP", map[string]string{"APP_PORT": "8080"}, "port", int64(8080)},
		{"a prefixed string", "APP", map[string]string{"APP_HOST": "kitsune"}, "host", "kitsune"},
		{"a compound key", "APP", map[string]string{"APP_LOG_LEVEL": "debug"}, "log_level", "debug"},
		{"an already-lowercase key", "app", map[string]string{"app_port": "1"}, "port", int64(1)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for key, val := range c.env {
			t.Setenv(key, val)
		}

		got, err := envSource{prefix: c.prefix}.Load()
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if got[c.wantKey] != c.wantVal {
			t.Errorf("%q = %#v, want %#v", c.wantKey, got[c.wantKey], c.wantVal)
		}
		//: the prefixed spelling must be gone; leaving both would double every
		//: key in the merged configuration.
		for key := range c.env {
			if _, still := got[key]; still {
				t.Errorf("Load kept the unstripped key %q", key)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: a Source that read nothing must still return a usable map, since the
	//: loader ranges over it without a nil check.
	got, err := envSource{prefix: "SDK_ABSENT_NAMESPACE"}.Load()
	if err != nil {
		t.Fatalf("Load on an empty namespace = %v, want nil", err)
	}
	if got == nil {
		t.Error("Load returned a nil map for an empty namespace")
	}
	//: the scan reads the real environment, so a process with none would make
	//: every case above vacuous.
	if len(os.Environ()) == 0 {
		t.Error("the process has no environment — the cases above proved nothing")
	}
}
