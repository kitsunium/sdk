//go:build unix

package kit_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// shortDir is a private directory short enough for a socket path (macOS's
// sun_path is the shortest, ADR 0094).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, dirErr := os.MkdirTemp("/tmp", "kitp")
	if dirErr != nil {
		t.Fatal(dirErr)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("removing %s: %v", dir, err)
		}
	})
	return dir
}

// echo answers each line of a connection in upper case, until the client
// closes it or the listener stops.
var echo kit.ListenHandler = func(_ context.Context, c *ipc.Conn) error {
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if _, err := c.Write([]byte(strings.ToUpper(line))); err != nil {
			return err
		}
	}
}

// daemonApp is a daemon: one listener, no HTTP.
func daemonApp(t *testing.T, sock string, opts ...kit.AppConfigurer) (*kit.App, *kit.Listener) {
	t.Helper()
	svc := kit.NewService("render", "Renders lines for its clients.")
	l := svc.Listen("socket", "render/v1", echo, kit.SocketPath(sock))
	app := kit.NewApp("statusline", svc).With(append([]kit.AppConfigurer{
		kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard), kit.Env(kit.EnvProduction),
	}, opts...)...)
	return app, l
}

// A daemon serves its listener and opens no HTTP port; its clients reach it
// through the private socket, and a connection is a span on the listener.
func TestADaemonServesItsListenerAndNoHTTP(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	app, l := daemonApp(t, sock)
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	if app.URL() != "" {
		t.Errorf("a daemon has a URL: %q", app.URL())
	}
	if l.Path() != sock {
		t.Errorf("the listener listens at %q, want %q", l.Path(), sock)
	}
	c, cErr := l.Dial(t.Context())
	if cErr != nil {
		t.Fatal(cErr)
	}
	if _, err := c.Write([]byte("status\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := bufio.NewReader(c).ReadString('\n'); err != nil || got != "STATUS\n" {
		t.Errorf("the daemon answered %q, %v", got, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	g := app.Graph()
	n := g.Node("render/listener/socket")
	if n == nil || n.Listener == nil || n.Listener.Contract != "render/v1" || n.Listener.Network != model.NetworkLocal {
		t.Fatalf("the listener's node: %+v", n)
	}
	for _, comp := range g.Runtime.Components {
		if comp.Name == "http" {
			t.Errorf("a daemon started an HTTP component: %+v", comp)
		}
	}
}

// Stopping the daemon ends the handlers — their connection is closed — and
// removes the socket.
func TestStoppingADaemonEndsItsConnections(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	app, l := daemonApp(t, sock)
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, cErr := l.Dial(t.Context())
	if cErr != nil {
		t.Fatal(cErr)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop with a connection open: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Stop waited for a handler that never ends by itself")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the socket survived the stop: %v", err)
	}
}

// A daemon launched on demand stops itself once no client came for its idle
// time — and not while a connection is open.
//
// Goroutine lifecycle: one goroutine runs the app and reports on a buffered
// channel; it ends with the idle stop, which the test waits for.
func TestADaemonStopsWhenIdle(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	app, l := daemonApp(t, sock, kit.IdleStop(150*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	var c *ipc.Conn
	deadline := time.Now().Add(5 * time.Second)
	for c == nil && time.Now().Before(deadline) {
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
		t.Fatalf("the daemon stopped with a client connected: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the idle stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop once idle")
	}
}

// A second process of a singleton app refuses to start, and the first one's
// stop lets a third start.
func TestASingletonRefusesASecondProcess(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("RUNTIME_DIRECTORY", "")
	first, _ := daemonApp(t, filepath.Join(dir, "a", "d.sock"), kit.Singleton("daemon"))
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, _ := daemonApp(t, filepath.Join(dir, "b", "d.sock"), kit.Singleton("daemon"))
	if err := second.Start(t.Context()); !errs.HasCode(err, kit.CodeSingletonHeld) {
		t.Fatalf("a second singleton started: %v", err)
	}
	if err := first.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	third, _ := daemonApp(t, filepath.Join(dir, "c", "d.sock"), kit.Singleton("daemon"))
	if err := third.Start(t.Context()); err != nil {
		t.Fatalf("the lock outlived its holder: %v", err)
	}
	if err := third.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// A profile that serves no HTTP refuses an endpoint, and an idle stop outside
// a daemon; the start names them all at once.
func TestAProfileRefusesWhatItCannotRun(t *testing.T) {
	svc := kit.NewService("web", "Serves a page.")
	svc.Endpoint("GET /page", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	app := kit.NewApp("x", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard))
	var de *kit.DiagnosticsError
	if err := app.Start(t.Context()); !errors.As(err, &de) || !mentionsAll(de, "web/endpoint/", "daemon profile") {
		t.Fatalf("a daemon with an endpoint: %v", err)
	}
	bad := kit.NewApp("y", kit.NewService("idle", "Nothing.")).With(kit.IdleStop(time.Second), kit.InMemory(), kit.Logs(io.Discard))
	if err := bad.Start(t.Context()); !errors.As(err, &de) || !mentionsAll(de, "IdleStop") {
		t.Fatalf("an idle stop on a server: %v", err)
	}
}

func mentionsAll(de *kit.DiagnosticsError, words ...string) bool {
	var all strings.Builder
	for _, d := range de.Diagnostics {
		all.WriteString(d.Message + "\n")
	}
	for _, w := range words {
		if !strings.Contains(all.String(), w) {
			return false
		}
	}
	return true
}

// Main runs a CLI command in the CLI profile: once, with its arguments, and
// its status is the process's; the listener a CLI run never needs is not
// opened.
func TestMainRunsACLICommand(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	svc := kit.NewService("render", "Renders lines.")
	l := svc.Listen("socket", "render/v1", echo, kit.SocketPath(sock))
	var got []string
	svc.CLI("status", "Print the status.", func(ctx context.Context, args []string, std kit.StdioValue) int {
		got = args
		if _, err := io.WriteString(std.Out, "ok\n"); err != nil {
			return 1
		}
		if l.Path() != "" {
			return 9
		}
		return 3
	})
	app := kit.NewApp("statusline", svc).With(kit.Profile(model.ProfileDaemon), kit.InMemory(), kit.Logs(io.Discard))
	if status := app.Main(t.Context(), []string{"status", "--json"}); status != 3 {
		t.Fatalf("Main = %d, want the command's 3 (9: a listener was opened)", status)
	}
	if strings.Join(got, " ") != "--json" {
		t.Errorf("the command got %q", got)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("a CLI run opened the listener's socket: %v", err)
	}
}

// A binary selects its role by its leading arguments, and every role's graph
// draws the binary, its roles and the contract between them.
func TestABinaryIsDrawnWithItsRolesAndTheirContract(t *testing.T) {
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	daemon, _ := daemonApp(t, sock)
	client := kit.NewService("client", "Asks the daemon.")
	var ran string
	client.CLI("show", "Show the line.", func(context.Context, []string, kit.StdioValue) int { ran = "show"; return 0 })
	render := kit.NewApp("statusline-client", client).With(kit.Profile(model.ProfileCLI), kit.InMemory(), kit.Logs(io.Discard))
	bin := kit.NewBinary("statusline", "The status line.").
		Role("render", render).
		Role("daemon", daemon, "daemon").
		Talks("render", "daemon", "render/v1")
	if status := bin.Main(t.Context(), []string{"show"}); status != 0 || ran != "show" {
		t.Fatalf("the default role's command: status %d, ran %q", status, ran)
	}
	g := daemon.Graph()
	for _, id := range []string{"binary:statusline", "binary:statusline/role/render", "binary:statusline/role/daemon"} {
		if g.Node(id) == nil {
			t.Errorf("the graph lacks %s", id)
		}
	}
	if r := g.Node("binary:statusline/role/daemon"); r == nil || r.Role.Profile != model.ProfileDaemon || strings.Join(r.Role.Args, " ") != "daemon" {
		t.Errorf("the daemon role: %+v", r)
	}
	e := g.Edge(model.EdgeID("binary:statusline/role/render", model.EdgeContracts, "binary:statusline/role/daemon", ""))
	if e == nil || e.Contract != "render/v1" || !e.Declared {
		t.Errorf("the contract edge: %+v", e)
	}
	if g.Edge(model.EdgeID("binary:statusline/role/daemon", model.EdgeRuns, "render/listener/socket", "")) == nil {
		t.Error("the daemon role does not run its listener")
	}
	var buf bytes.Buffer
	for _, n := range g.Nodes {
		if _, err := model.ParseID(n.ID); err != nil {
			buf.WriteString(n.ID + " ")
		}
	}
	if buf.Len() > 0 {
		t.Errorf("IDs outside the grammar: %s", buf.String())
	}
}

// Main's help lists the product's own CLI commands, each with its one-line
// help.
func TestHelpListsTheProductsCommands(t *testing.T) {
	svc := kit.NewService("render", "Renders lines.")
	svc.CLI("status", "Print the status.", func(context.Context, []string, kit.StdioValue) int { return 0 })
	app := kit.NewApp("statusline", svc).With(kit.InMemory(), kit.Logs(io.Discard))
	out := captureStdout(t, func() {
		if code := app.Main(t.Context(), []string{"help"}); code != 0 {
			t.Errorf("help exits %d", code)
		}
	})
	if !strings.Contains(out, "Commands of the product:") || !strings.Contains(out, "statusline status") ||
		!strings.Contains(out, "Print the status.") {
		t.Errorf("the help does not list the command:\n%s", out)
	}
}
