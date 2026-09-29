package kit

import (
	"net/http"
	"net/http/httptest"
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
		{"dev binds loopback", map[string]string{"KIT_ENV": "dev"}, "127.0.0.1:4000", true, false, false},
		{"dev keeps PORT on loopback", map[string]string{"KIT_ENV": "dev", "PORT": "8080"}, "127.0.0.1:8080", true, false, false},
		{"dev with an explicit address", map[string]string{"KIT_ENV": "dev", "KIT_ADDR": "0.0.0.0:9"}, "0.0.0.0:9", true, false, false},
		{"dev opts in to remote clients", map[string]string{"KIT_ENV": "dev", "KIT_STUDIO_REMOTE": "on"}, "127.0.0.1:4000", true, true, false},
		{"dev without the Studio", map[string]string{"KIT_ENV": "dev", "KIT_STUDIO": "off", "KIT_STUDIO_REMOTE": "on"}, "127.0.0.1:4000", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := resolveConfig(&appOptions{}, env(c.vars))
			if cfg.addr != c.addr || cfg.studio.on != c.studio || cfg.studio.remote != c.remote || (cfg.dataDir == "") != c.memory {
				t.Errorf("addr %q studio %v remote %v dataDir %q", cfg.addr, cfg.studio.on, cfg.studio.remote, cfg.dataDir)
			}
		})
	}
}

// The Host header is the client's to choose; the Studio also checks where the
// request comes from.
func TestStudioAnswersThisMachineOnly(t *testing.T) {
	a := &App{cfg: config{allowedHosts: []string{"localhost", "127.0.0.1", "::1"}}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for _, c := range []struct {
		remote string
		opt    bool
		want   int
	}{
		{"127.0.0.1:5555", false, http.StatusTeapot},
		{"[::1]:5555", false, http.StatusTeapot},
		{"192.168.1.9:5555", false, http.StatusForbidden},
		{"172.17.0.1:5555", true, http.StatusTeapot},
	} {
		a.cfg.studio.remote = c.opt
		req := httptest.NewRequest("GET", "http://localhost:4000/_kit/api/graph", nil)
		req.RemoteAddr = c.remote
		rec := httptest.NewRecorder()
		a.hostGuard(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s (opt-in %v): %d, want %d", c.remote, c.opt, rec.Code, c.want)
		}
	}
}

func TestSettingsSayWhereEachValueComesFrom(t *testing.T) {
	env := map[string]string{"PORT": "8080", "KIT_SMTP_URL": "smtp://mailer:hunter2@smtp.example.com:587"}
	getenv := func(k string) string { return env[k] }
	set := settings(&appOptions{}, getenv, new(resolveConfig(&appOptions{}, getenv)))
	by := map[string]model.Setting{}
	for _, s := range set {
		by[s.Name] = s
		if strings.Contains(s.Value, "hunter2") {
			t.Fatalf("a setting shows the password: %+v", s)
		}
	}
	if s := by["KIT_ADDR"]; s.Value != ":8080" || s.From != model.SettingEnv {
		t.Errorf("KIT_ADDR from PORT: %+v", s)
	}
	if s := by["KIT_ENV"]; s.Value != EnvProduction || s.From != model.SettingDefault {
		t.Errorf("KIT_ENV by default: %+v", s)
	}
	// The SMTP URL is a secret: where it is found is said once the app has
	// opened the environment's stores, never here (boot_test.go).
	if s, ok := by["KIT_SMTP_URL"]; ok {
		t.Errorf("KIT_SMTP_URL is read from the secret stores, not the settings: %+v", s)
	}
	if s := by["KIT_DATA_DIR"]; s.Value != "memory" || s.From != model.SettingDefault {
		t.Errorf("no data directory in production: %+v", s)
	}
}

// kit dev's label is an origin and nothing else: a variable it set for this
// launch reads "kit dev" when the environment is what wins, the code still
// wins over it, a name it could not have set is dropped, and no value moves.
func TestKitDevLabelsOnlyWhatItForced(t *testing.T) {
	env := map[string]string{
		model.VarEnv: "dev", model.VarAddr: "127.0.0.1:4000", model.VarDataDir: "/srv/app/.kit/data", model.VarLogLevel: "debug",
		model.VarDevForced: "KIT_ENV, KIT_ADDR,KIT_DATA_DIR,KIT_LOG_LEVEL,PATH",
	}
	getenv := func(k string) string { return env[k] }
	by := func(o appOptions) map[string]model.Setting {
		out := map[string]model.Setting{}
		for _, s := range settings(&o, getenv, new(resolveConfig(&o, getenv))) {
			out[s.Name] = s
		}
		return out
	}
	got := by(appOptions{})
	for _, name := range []string{model.VarEnv, model.VarAddr, model.VarDataDir} {
		if got[name].From != model.SettingKitDev || got[name].Value != env[name] && name != model.VarEnv {
			t.Errorf("%s: %+v, want kit dev and the value unchanged", name, got[name])
		}
	}
	if got[model.VarLogLevel].From != model.SettingEnv {
		t.Errorf("KIT_LOG_LEVEL is not a name kit dev sets: %+v", got[model.VarLogLevel])
	}
	if s := got[model.VarAddr]; s.Option != "kit.Listen" || s.Flag != "-addr" {
		t.Errorf("KIT_ADDR says how to change it: %+v", s)
	}
	if s := by(appOptions{addr: new("127.0.0.1:0")})[model.VarAddr]; s.From != model.SettingOption {
		t.Errorf("the code wins over kit dev: %+v", s)
	}
	if s := by(appOptions{memory: true})[model.VarDataDir]; s.From != model.SettingOption || s.Option != "kit.InMemory" {
		t.Errorf("kit.InMemory wins and is named: %+v", s)
	}
	delete(env, model.VarDevForced)
	if s := by(appOptions{})[model.VarAddr]; s.From != model.SettingEnv {
		t.Errorf("without kit dev's list, the environment is the environment: %+v", s)
	}
}
