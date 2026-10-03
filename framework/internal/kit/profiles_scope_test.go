//go:build unix

package kit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// cliApp is an app whose one product command records the arguments it got
// and returns status; opts tune the command.
func cliApp(t *testing.T, status int, got *[]string, opts ...kit.CommandLineConfigurer) *kit.App {
	t.Helper()
	svc := kit.NewService("line", "Prints a status line.")
	svc.CLI("render", "Render the line.", func(_ context.Context, args []string, _ kit.StdioValue) int {
		*got = append([]string{"ran"}, args...)
		return status
	}, opts...)
	return kit.NewApp("statusline", svc).With(kit.InMemory(), kit.Logs(io.Discard))
}

// The default command runs with no argument at all, and with every argument
// when the first names no command; a product command and Main's own still
// win when named.
func TestTheDefaultCommandTakesWhatNamesNoCommand(t *testing.T) {
	var got []string
	app := cliApp(t, 0, &got, kit.DefaultCommand())
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "ran"},
		{[]string{"--json", "x"}, "ran --json x"},
		{[]string{"render", "y"}, "ran y"},
	} {
		got = nil
		if status := app.Main(t.Context(), c.args); status != 0 {
			t.Errorf("Main(%q) = %d", c.args, status)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("Main(%q) ran %q, want %q", c.args, got, c.want)
		}
	}
	got = nil
	if status := app.Main(t.Context(), []string{"config"}); got != nil || status == 2 {
		t.Errorf("Main's own command went to the default: ran %q, status %d", got, status)
	}
}

// Without a default command an unknown first argument is still a usage
// error: the default is opt-in.
func TestWithoutADefaultAnUnknownCommandIsAUsageError(t *testing.T) {
	var got []string
	if status := cliApp(t, 0, &got).Main(t.Context(), []string{"nope"}); status != 2 || got != nil {
		t.Errorf("Main(nope) = %d, ran %q", status, got)
	}
}

// Two default commands refuse the start: Main can run only one.
func TestTwoDefaultCommandsAreRefused(t *testing.T) {
	svc := kit.NewService("line", "Prints a status line.")
	noop := func(context.Context, []string, kit.StdioValue) int { return 0 }
	svc.CLI("a", "A.", noop, kit.DefaultCommand())
	svc.CLI("b", "B.", noop, kit.DefaultCommand())
	app := kit.NewApp("two", svc).With(kit.InMemory(), kit.Logs(io.Discard))
	var de *kit.DiagnosticsError
	if err := app.Start(t.Context()); !errors.As(err, &de) || !mentionsAll(de, "line/cli/a", "line/cli/b", "default command") {
		t.Fatalf("two defaults: %v", err)
	}
}

// A fail-safe command's status is always 0: a failure and a panic are logged,
// never the shell's problem.
func TestAFailSafeCommandAlwaysExitsZero(t *testing.T) {
	var got []string
	if status := cliApp(t, 3, &got, kit.FailSafe()).Main(t.Context(), []string{"render"}); status != 0 || got == nil {
		t.Errorf("a failing fail-safe command: status %d, ran %q", status, got)
	}
	svc := kit.NewService("line", "Prints a status line.")
	svc.CLI("render", "Render the line.", func(context.Context, []string, kit.StdioValue) int { panic("boom") },
		kit.FailSafe(), kit.DefaultCommand())
	app := kit.NewApp("statusline", svc).With(kit.InMemory(), kit.Logs(io.Discard))
	if status := app.Main(t.Context(), []string{"anything"}); status != 0 {
		t.Errorf("a panicking fail-safe command: status %d", status)
	}
}

// A scope key is the same for the same values and differs with them; PerEnv
// reads its variable, else its fallback, a leading ~/ being the home.
func TestAScopeKeyFollowsItsValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIT_TEST_CONF", "")
	scope := kit.PerEnv("KIT_TEST_CONF", "~/.claude")
	fallback, err := kit.ScopeKey(kit.PerUID, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIT_TEST_CONF", filepath.Join(home, ".claude"))
	same, err := kit.ScopeKey(kit.PerUID, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIT_TEST_CONF", filepath.Join(home, "other"))
	other, err := kit.ScopeKey(kit.PerUID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != same || same == other || len(same) != 16 {
		t.Errorf("keys: fallback %s, same %s, other %s", fallback, same, other)
	}
	if _, err := kit.ScopeKey(kit.PerExecutable, kit.PerConfigDir); err != nil {
		t.Errorf("the executable and the config dir: %v", err)
	}
}

// A singleton per scope refuses a second process with the same values and
// lets one with other values run.
func TestASingletonPerScopeKeepsOneProcessPerValue(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("KIT_TEST_CONF", "/one")
	per := kit.SingletonPer(kit.PerUID, kit.PerEnv("KIT_TEST_CONF", ""))
	first, _ := daemonApp(t, filepath.Join(dir, "a", "d.sock"), per)
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	second, _ := daemonApp(t, filepath.Join(dir, "b", "d.sock"), per)
	if err := second.Start(t.Context()); !errs.HasCode(err, kit.CodeSingletonHeld) {
		t.Fatalf("a second process with the same scopes started: %v", err)
	}
	t.Setenv("KIT_TEST_CONF", "/two")
	third, _ := daemonApp(t, filepath.Join(dir, "c", "d.sock"), per)
	if err := third.Start(t.Context()); err != nil {
		t.Fatalf("a process with other scopes: %v", err)
	}
	if err := third.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// A socket per scope carries the scopes' key, and a client of another role
// finds it from the declaration and the daemon's app name.
func TestASocketPerScopeIsFoundFromTheDeclaration(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("RUNTIME_DIRECTORY", "")
	svc := kit.NewService("render", "Renders lines for its clients.")
	l := svc.Listen("socket", "render/v1", echo, kit.SocketPer(kit.PerUID))
	app := kit.NewApp("sl", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard), kit.Env(kit.EnvProduction))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	key, err := kit.ScopeKey(kit.PerUID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := l.SocketPathIn("sl")
	if err != nil || path != l.Path() || !strings.HasSuffix(path, "render-socket-"+key+".sock") {
		t.Fatalf("SocketPathIn = %q, %v; the daemon listens on %q", path, err, l.Path())
	}
	if free, err := kit.SocketPathFor("sl", "render", "socket", kit.PerUID); err != nil || free != path {
		t.Errorf("SocketPathFor = %q, %v; want %q", free, err, path)
	}
	c, err := l.DialIn(t.Context(), "sl")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Error(err)
	}
	bad := kit.NewService("bad", "Both.")
	bad.Listen("socket", "render/v1", echo, kit.SocketPath("/tmp/x.sock"), kit.SocketPer(kit.PerUID))
	var de *kit.DiagnosticsError
	both := kit.NewApp("both", bad).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard))
	if err := both.Start(t.Context()); !errors.As(err, &de) || !mentionsAll(de, "SocketPer") {
		t.Errorf("a socket path and scopes: %v", err)
	}
}

// A daemon whose activity says it is busy does not stop when idle of
// connections; once the activity says false, it stops after its idle time.
//
// Goroutine lifecycle: one goroutine runs the app and reports on a buffered
// channel; it ends with the idle stop, which the test waits for.
func TestADaemonWaitsForItsActivities(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	var sessions atomic.Int64
	sessions.Store(1)
	svc := kit.NewService("render", "Renders lines for its clients.")
	svc.Listen("socket", "render/v1", echo, kit.SocketPath(sock))
	svc.Activity(func(context.Context) bool { return sessions.Load() > 0 })
	app := kit.NewApp("statusline", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard),
		kit.Env(kit.EnvProduction), kit.IdleStop(100*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("the daemon stopped with a session open: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	sessions.Store(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the idle stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop once its sessions ended")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the daemon: %v", err)
	}
}

// A CLI run starts only what its command reads: with nothing but the
// command declared — in dev, where a server would open the Studio and ask git
// about the build —, it writes no log line, creates no file, starts no
// lifecycle component and leaves no goroutine behind. A status line runs it
// on every render.
func TestACLIRunStartsOnlyWhatItsCommandReads(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("KIT_ENV", "dev")
	t.Setenv("KIT_DATA_DIR", "")
	var logs bytes.Buffer
	var app *kit.App
	var components []model.Component
	svc := kit.NewService("line", "Prints a status line.")
	svc.CLI("render", "Render the line.", func(context.Context, []string, kit.StdioValue) int {
		if rt := app.Graph().Runtime; rt != nil {
			components = rt.Components
		}
		return 0
	}, kit.DefaultCommand(), kit.FailSafe())
	app = kit.NewApp("statusline", svc).With(kit.Logs(&logs))
	before := runtime.NumGoroutine()
	if status := app.Main(t.Context(), []string{"--width", "80"}); status != 0 {
		t.Fatalf("Main = %d", status)
	}
	if logs.Len() != 0 {
		t.Errorf("a CLI run logged:\n%s", logs.String())
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("a CLI run created %v (%v)", entries, err)
	}
	if len(components) != 0 {
		t.Errorf("a CLI run started components: %+v", components)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before the run, %d after", before, after)
	}
}

// A CLI command of a singleton app runs while the daemon holds the lock —
// `<binary> daemon status` is useful exactly then —; a second daemon is still
// refused.
func TestACLICommandDoesNotTakeTheSingleton(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("RUNTIME_DIRECTORY", "")
	per := kit.SingletonPer(kit.PerUID)
	daemon, _ := daemonApp(t, filepath.Join(dir, "a", "d.sock"), per)
	if err := daemon.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := daemon.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	svc := kit.NewService("render", "Renders lines for its clients.")
	ran := false
	svc.CLI("status", "Say whether the daemon runs.", func(context.Context, []string, kit.StdioValue) int {
		ran = true
		return 0
	})
	client := kit.NewApp("statusline", svc).With(kit.Profile(model.ProfileDaemon), per, kit.InMemory(), kit.Logs(io.Discard))
	if status := client.Main(t.Context(), []string{"status"}); status != 0 || !ran {
		t.Errorf("status while the daemon runs: %d, ran %v", status, ran)
	}
	second, _ := daemonApp(t, filepath.Join(dir, "b", "d.sock"), per)
	if err := second.Start(t.Context()); !errs.HasCode(err, kit.CodeSingletonHeld) {
		t.Errorf("a second daemon: %v", err)
	}
}

// Two products never share a runtime directory: a role of a binary keeps its
// sockets and locks under the binary's name, not under its app's — an app
// named "daemon" in two products would otherwise meet in one directory.
func TestARolesSocketLivesUnderItsBinary(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("RUNTIME_DIRECTORY", "")
	svc := kit.NewService("render", "Renders lines for its clients.")
	l := svc.Listen("socket", "render/v1", echo)
	daemon := kit.NewApp("daemon", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard), kit.Env(kit.EnvProduction))
	kit.NewBinary("sl", "A status line.").Role("daemon", daemon, "daemon")
	if err := daemon.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := daemon.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	want, err := kit.SocketPathFor("sl", "render", "socket")
	if err != nil || l.Path() != want || filepath.Dir(want) != filepath.Join(dir, "sl") {
		t.Errorf("the daemon listens on %q; SocketPathFor says %q (%v)", l.Path(), want, err)
	}
}

// A handler ends its own app's run with kit.Stop: a daemon told "stop" by
// its client. A context that runs in no app stops nothing.
//
// Goroutine lifecycle: one goroutine runs the app and reports on a buffered
// channel; the handler's Stop ends it, which the test waits for.
func TestAHandlerStopsItsApp(t *testing.T) {
	if kit.Stop(context.Background()) {
		t.Error("Stop outside an app reported a stop")
	}
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	svc := kit.NewService("render", "Renders lines for its clients.")
	l := svc.Listen("socket", "render/v1", func(ctx context.Context, _ *ipc.Conn) error {
		if !kit.Stop(ctx) {
			return errors.New("the handler's context runs in no app")
		}
		return nil
	}, kit.SocketPath(sock))
	app := kit.NewApp("statusline", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard), kit.Env(kit.EnvProduction))
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	var c *ipc.Conn
	for deadline := time.Now().Add(5 * time.Second); c == nil && time.Now().Before(deadline); {
		if conn, err := l.Dial(t.Context()); err == nil {
			c = conn
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if c == nil {
		t.Fatal("the daemon never listened")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop when its handler asked")
	}
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Log(err)
	}
}

// probeLine is what a daemon that keeps a store keeps.
type probeLine struct {
	ID string `json:"id"`
}

// A daemon that keeps nothing says it runs, and says nothing of a data
// directory it does not need; one that keeps a store still hears it.
func TestADaemonWithoutAStoreIsNotWarnedOfMemory(t *testing.T) {
	t.Setenv("KIT_DATA_DIR", "")
	run := func(keeps bool) string {
		var logs bytes.Buffer
		svc := kit.NewService("render", "Renders lines for its clients.")
		svc.Listen("socket", "render/v1", echo, kit.SocketPath(filepath.Join(shortDir(t), "run", "d.sock")))
		if keeps {
			svc.Store("lines", func(l probeLine) string { return l.ID })
		}
		app := kit.NewApp("statusline", svc).With(kit.Profile(model.ProfileDaemon), kit.Logs(&logs), kit.Env(kit.EnvProduction))
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
		return logs.String()
	}
	if out := run(false); strings.Contains(out, "no data directory") || !strings.Contains(out, `"running"`) || strings.Contains(out, `"url"`) {
		t.Errorf("a daemon with no store logged:\n%s", out)
	}
	if out := run(true); !strings.Contains(out, "no data directory") {
		t.Errorf("a daemon with a store was not warned:\n%s", out)
	}
}
