package service

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// stubBus is a BusWatcher the settings tests never call.
type stubBus struct{}

func (stubBus) WatchName(ctx context.Context, _ string) (<-chan string, error) {
	out := make(chan string)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out, nil
}

// dbusConfig is a Type=dbus unit on a stub watcher.
func dbusConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Name: "u", Type: coresvc.TypeDbus, BusName: "org.example.Svc", BusWatcher: stubBus{},
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	}
}

// TestDbusSettings: S0b's refusals of Type=dbus and BusName= are lifted on
// Linux (kitsunium/sdk#737); elsewhere Type=dbus is DIRECTIVE_UNSUPPORTED
// naming the type and the platform. A Type=dbus unit needs a valid name and
// a BusWatcher; a unit with a name and no Type= is Type=dbus, as systemd
// reads it; every other type accepts a valid name and does not watch it.
func TestDbusSettings(t *testing.T) {
	cfg := dbusConfig(t)
	s, err := resolve(&cfg)
	if runtime.GOOS != osLinux {
		if !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "directive") != "Type" ||
			field(err, "value") != "dbus" || field(err, "issue") != runtime.GOOS {
			t.Fatalf("Type=dbus on %s: %v, want DIRECTIVE_UNSUPPORTED naming Type and the platform", runtime.GOOS, err)
		}
		return
	}
	if err != nil || s.typ != coresvc.TypeDbus || s.busName != "org.example.Svc" {
		t.Fatalf("Type=dbus: %v (%+v)", err, s)
	}
	for _, tc := range []struct {
		edit      func(*Config)
		directive string
	}{
		{func(c *Config) { c.BusName = "" }, "BusName"},
		{func(c *Config) { c.BusWatcher = nil }, "BusWatcher"},
		{func(c *Config) { c.BusName = ":1.42" }, "BusName"},
	} {
		c := dbusConfig(t)
		tc.edit(&c)
		if _, err := resolve(&c); !hasCode(err, coresvc.CodeConfigInvalid) || field(err, "directive") != tc.directive {
			t.Errorf("%s: %v, want CONFIG_INVALID naming it", tc.directive, err)
		}
	}
	byDefault := dbusConfig(t)
	byDefault.Type = coresvc.TypeDefault
	if s, err := resolve(&byDefault); err != nil || s.typ != coresvc.TypeDbus {
		t.Fatalf("BusName= without Type=: %v, want Type=dbus", err)
	}
	simple := Config{Name: "u", Type: coresvc.TypeSimple, BusName: "org.example.Svc", ExecStart: dbusConfig(t).ExecStart}
	if s, err := resolve(&simple); err != nil || s.busName != "" {
		t.Fatalf("Type=simple with BusName=: %v (watched %q), want accepted and not watched", err, s.busName)
	}
}

// TestBusNames: the well-known names the D-Bus specification allows, and
// those it does not — a refusal names BusName=.
func TestBusNames(t *testing.T) {
	for _, name := range []string{"a.b", "org.freedesktop.login1", "org.example.my-service", "_a.b_1", "A.B-c.D9"} {
		if !busNameValid(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{
		"", "org", ":1.42", ".org.x", "org..x", "org.x.", "org.1x", "1org.x", "org.x$", "org.x y", "org/x.y",
		"org.é.x", strings.Repeat("a.", 128),
	} {
		if busNameValid(name) {
			t.Errorf("%q accepted", name)
		}
		simple := Config{Name: "u", BusName: name, Type: coresvc.TypeSimple, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}}
		if _, err := resolve(&simple); name != "" && (!hasCode(err, coresvc.CodeConfigInvalid) || field(err, "directive") != "BusName") {
			t.Errorf("BusName=%q: %v, want CONFIG_INVALID naming BusName", name, err)
		}
	}
}

// TestDbusInstanceRefused: a Type=dbus template is no Accept=yes template —
// its instances would share one bus name, which systemd refuses to a second
// unit —; the connection and the socket are closed with the refusal.
func TestDbusInstanceRefused(t *testing.T) {
	if runtime.GOOS != osLinux {
		t.Skip("Type=dbus is refused before Accept= off Linux: TestDbusSettings")
	}
	cfg := dbusConfig(t)
	conn, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if _, err := newInstance(&cfg, conn); !hasCode(err, coresvc.CodeConfigInvalid) || field(err, "directive") != "Accept" {
		t.Fatalf("a Type=dbus instance: %v, want CONFIG_INVALID naming Accept", err)
	}
	if err := conn.Close(); err == nil {
		t.Fatal("the connection is still open after the refusal")
	}
}
