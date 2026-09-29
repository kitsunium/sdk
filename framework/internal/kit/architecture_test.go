package kit_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// The web: a frontend, and an auth handler in front of an account endpoint.
var Web = kit.NewService("web", "The web app, for the architecture's tests.")

type WebCredentials struct {
	Session string `cookie:"web_session"`
}

var (
	// kit.Root(".") serves the whole file system — and keeps go vet's printf
	// check quiet: with Go 1.27 generic methods on *Service, it takes Static
	// for the printf wrapper problem, whose format is its third argument.
	_ = Web.Static("app", "/", webFiles, kit.Root("."))
	_ = Web.AuthHandler("session", webSession)
	_ = Web.Mailer("mail", kit.From("Web", "hello@web.localhost"))
)

var webFiles = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>web</title>")}, "app.js": {Data: []byte("1")}}

// webSession knows nobody.
func webSession(context.Context, WebCredentials) (kit.UID, struct{}, error) {
	return "", struct{}{}, nil
}

func containerOf(arch *model.Architecture, kind string) *model.Container {
	for i := range arch.Containers {
		if arch.Containers[i].Kind == kind {
			return &arch.Containers[i]
		}
	}
	return nil
}

func linkOf(arch *model.Architecture, from, to string) *model.Link {
	for i := range arch.Links {
		if arch.Links[i].From == from && arch.Links[i].To == to {
			return &arch.Links[i]
		}
	}
	return nil
}

// A product's context and containers follow from what it declares.
func TestArchitectureFollowsTheDeclarations(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := kit.NewApp("web", Web, Shop, Audit).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	call(t, app, "GET /search?q=x", noBody, "Referer", app.URL()+"/")
	arch := app.Graph().Architecture
	if arch == nil {
		t.Fatal("no architecture")
	}
	if len(arch.People) != 1 || arch.People[0].ID != "person:user" || !strings.Contains(arch.People[0].Doc, "browser") {
		t.Errorf("people %+v", arch.People)
	}
	var systems []string
	for _, s := range arch.Systems {
		systems = append(systems, s.ID+"="+s.Name)
	}
	if !slices.Equal(systems, []string{"system:clients=API clients", "system:mailbox=Studio mailbox (dev capture)"}) {
		t.Errorf("systems %v", systems)
	}

	proc := containerOf(arch, model.ContainerProcess)
	if proc == nil || proc.Name != "web" || !strings.HasPrefix(proc.Technology, "Go 1.") || !strings.HasSuffix(proc.Technology, "· kit · net/http") ||
		proc.Location != strings.TrimPrefix(app.URL(), "http://") || !slices.Equal(proc.Nodes, []string{"audit", "shop", "web"}) {
		t.Errorf("process %+v", proc)
	}
	spa := containerOf(arch, model.ContainerSPA)
	if spa == nil || spa.ID != "container:spa:web/frontend/app" || spa.Location != app.URL()+"/" || !strings.Contains(spa.Doc, "2 files") {
		t.Errorf("spa %+v", spa)
	}
	vol := containerOf(arch, model.ContainerVolume)
	if vol == nil || vol.Location != dir || !slices.Contains(vol.Nodes, "shop/store/items") || !slices.Contains(vol.Nodes, "audit/subscription/record") || !slices.Contains(vol.Nodes, "web/mailer/mail") {
		t.Errorf("volume %+v", vol)
	}
	if containerOf(arch, model.ContainerMemory) != nil {
		t.Error("nothing is in memory only")
	}
	if l := linkOf(arch, "person:user", spa.ID); l == nil || l.Label != "Uses" {
		t.Errorf("user → spa %+v", l)
	}
	if l := linkOf(arch, spa.ID, "container:process"); l == nil || !slices.Equal(l.Nodes, []string{"shop/endpoint/Search"}) {
		t.Errorf("spa → process names the endpoints its pages called: %+v", l)
	}
	if l := linkOf(arch, "system:clients", "container:process"); l == nil || !slices.Contains(l.Nodes, "shop/endpoint/CreateItem") || slices.Contains(l.Nodes, "shop/endpoint/Count") {
		t.Errorf("clients → process lists the public endpoints: %+v", l)
	}
	if l := linkOf(arch, "container:process", "container:volume"); l == nil || l.Technology == "" {
		t.Errorf("process → volume %+v", l)
	}
	if l := linkOf(arch, "container:process", "system:mailbox"); l == nil || !slices.Equal(l.Nodes, []string{"web/mailer/mail"}) {
		t.Errorf("process → mailbox %+v", l)
	}
}

// Outside dev, the architecture says where nothing is: no address, no path.
func TestArchitectureDisclosesNoLocationOutsideDev(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := kit.NewApp("web", Web, Shop, Audit).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	raw, rawErr := json.Marshal(app.Graph().Architecture)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	for _, leak := range []string{dir, strings.TrimPrefix(app.URL(), "http://"), "Studio mailbox"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("production architecture discloses %q: %s", leak, raw)
		}
	}
}

// The graph command draws the architecture without running the product.
func TestArchitectureWithoutRunning(t *testing.T) {
	app := kit.NewApp("shop", Shop, Audit).With(kit.InMemory(), kit.Logs(io.Discard))
	out := captureStdout(t, func() {
		if code := app.Main(t.Context(), []string{"graph", "-static=false"}); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	var g model.Graph
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		t.Fatal(err)
	}
	arch := g.Architecture
	if arch == nil || containerOf(arch, model.ContainerProcess) == nil || containerOf(arch, model.ContainerMemory) == nil || g.Runtime != nil {
		t.Fatalf("architecture without running: %+v", arch)
	}
	if len(arch.People) != 0 {
		t.Errorf("no frontend and no auth: no person, got %+v", arch.People)
	}
}
