package app

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestConfigIsClosedByDefault(t *testing.T) {
	for _, c := range []struct {
		name                   string
		vars                   map[string]string
		addr                   string
		studio, remote, memory bool
	}{
		{"unset is production", map[string]string{}, ":4000", false, false, true},
		{"production ignores KIT_STUDIO", map[string]string{"KIT_STUDIO": "on", "KIT_STUDIO_REMOTE": "on"}, ":4000", false, false, true},
		{"production honours PORT on every interface", map[string]string{"PORT": "8080"}, ":8080", false, false, true},
		{"dev binds loopback", map[string]string{"TODO_ENV": "dev"}, "127.0.0.1:4000", true, false, false},
		{"dev keeps PORT on loopback", map[string]string{"TODO_ENV": "dev", "PORT": "8080"}, "127.0.0.1:8080", true, false, false},
		{"dev with an explicit address", map[string]string{"TODO_ENV": "dev", "TODO_ADDR": "0.0.0.0:9"}, "0.0.0.0:9", true, false, false},
		{"dev opts in to remote clients", map[string]string{"TODO_ENV": "dev", "KIT_STUDIO_REMOTE": "on"}, "127.0.0.1:4000", true, true, false},
		{"dev without the Studio", map[string]string{"TODO_ENV": "dev", "KIT_STUDIO": "off", "KIT_STUDIO_REMOTE": "on"}, "127.0.0.1:4000", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := resolveConfig(&appOptions{}, "todo", env(c.vars))
			if cfg.addr != c.addr || cfg.studio.on != c.studio || cfg.studio.remote != c.remote || (cfg.dataDir == "") != c.memory {
				t.Errorf("addr %q studio %v remote %v dataDir %q", cfg.addr, cfg.studio.on, cfg.studio.remote, cfg.dataDir)
			}
		})
	}
}

func TestSettingsSayWhereEachValueComesFrom(t *testing.T) {
	env := map[string]string{"PORT": "8080", "TODO_SMTP_URL": "smtp://mailer:hunter2@smtp.example.com:587"}
	getenv := func(k string) string { return env[k] }
	set := settings(&appOptions{}, "todo", getenv, new(resolveConfig(&appOptions{}, "todo", getenv)))
	by := map[string]model.Setting{}
	for _, s := range set {
		by[s.Name] = s
		if strings.Contains(s.Value, "hunter2") {
			t.Fatalf("a setting shows the password: %+v", s)
		}
	}
	if s := by["TODO_ADDR"]; s.Value != ":8080" || s.From != model.SettingEnv {
		t.Errorf("KIT_ADDR from PORT: %+v", s)
	}
	if s := by["TODO_ENV"]; s.Value != EnvProduction || s.From != model.SettingDefault {
		t.Errorf("KIT_ENV by default: %+v", s)
	}
	// The SMTP URL is a secret: where it is found is said once the app has
	// opened the environment's stores, never here (boot_test.go).
	if s, ok := by["TODO_SMTP_URL"]; ok {
		t.Errorf("KIT_SMTP_URL is read from the secret stores, not the settings: %+v", s)
	}
	if s := by["TODO_DATA_DIR"]; s.Value != "memory" || s.From != model.SettingDefault {
		t.Errorf("no data directory in production: %+v", s)
	}
}

// kit dev's label is an origin and nothing else: a variable it set for this
// launch reads "kit dev" when the environment is what wins, the code still
// wins over it, a name it could not have set is dropped, and no value moves.
func TestKitDevLabelsOnlyWhatItForced(t *testing.T) {
	env := map[string]string{
		"TODO_ENV": "dev", "TODO_ADDR": "127.0.0.1:4000", "TODO_DATA_DIR": "/srv/app/.kit/data", "TODO_LOG_LEVEL": "debug",
		model.VarDevForced: "TODO_ENV, TODO_ADDR,TODO_DATA_DIR,TODO_LOG_LEVEL,PATH,KIT_ENV",
	}
	getenv := func(k string) string { return env[k] }
	by := func(o appOptions) map[string]model.Setting {
		out := map[string]model.Setting{}
		for _, s := range settings(&o, "todo", getenv, new(resolveConfig(&o, "todo", getenv))) {
			out[s.Name] = s
		}
		return out
	}
	got := by(appOptions{})
	for _, name := range []string{"TODO_ENV", "TODO_ADDR", "TODO_DATA_DIR"} {
		if got[name].From != model.SettingAppDev || got[name].Value != env[name] && name != "TODO_ENV" {
			t.Errorf("%s: %+v, want kit dev and the value unchanged", name, got[name])
		}
	}
	if got["TODO_LOG_LEVEL"].From != model.SettingEnv {
		t.Errorf("TODO_LOG_LEVEL is not a name kit dev sets: %+v", got["TODO_LOG_LEVEL"])
	}
	if s := got["TODO_ADDR"]; s.Option != "app.Listen" || s.Flag != "-addr" {
		t.Errorf("KIT_ADDR says how to change it: %+v", s)
	}
	if s := by(appOptions{addr: new("127.0.0.1:0")})["TODO_ADDR"]; s.From != model.SettingOption {
		t.Errorf("the code wins over kit dev: %+v", s)
	}
	if s := by(appOptions{memory: true})["TODO_DATA_DIR"]; s.From != model.SettingOption || s.Option != "app.InMemory" {
		t.Errorf("app.InMemory wins and is named: %+v", s)
	}
	delete(env, model.VarDevForced)
	if s := by(appOptions{})["TODO_ADDR"]; s.From != model.SettingEnv {
		t.Errorf("without kit dev's list, the environment is the environment: %+v", s)
	}
}

// The last of app.InMemory and app.DataDir given to an app says where its
// data is: a suite's helper that runs the product in memory runs it on disk
// with the one option a test adds (apptest.NewDisk), and an InMemory given
// after a DataDir still keeps everything in memory.
func TestTheLastOfInMemoryAndDataDirWins(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, c := range []struct {
		name string
		opts []AppConfigurer
		want string
	}{
		{"a data directory after memory", []AppConfigurer{InMemory(), DataDir("/data")}, "/data"},
		{"memory after a data directory", []AppConfigurer{DataDir("/data"), InMemory()}, ""},
	} {
		var o appOptions
		for _, opt := range c.opts {
			opt.appConfigure(&o)
		}
		if dir, _ := resolveDataDir(&o, getenv, true); dir != c.want {
			t.Errorf("%s: the data directory is %q; want %q", c.name, dir, c.want)
		}
	}
}
