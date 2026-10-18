package app_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// A product that is not a web product: a job and a hand-written loop, and no
// endpoint, frontend or auth handler.
var Batch = app.NewService("batch", "A job and a loop, and no HTTP, for the connectors' tests.")

var (
	_ = Batch.Every("tick", time.Hour, func(context.Context) error { return nil })
	_ = Batch.Go("watch", func(ctx context.Context) error { <-ctx.Done(); return nil })
)

// A product that declares no HTTP has no http connector, and its process is
// not a web server: kit's own port is drawn as kit's — the probes and the
// Studio — with its address in dev.
func TestAProductWithoutHTTP(t *testing.T) {
	a := app.NewApp("batch", Batch).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer a.Stop(context.Background())
	g := a.Graph()
	for _, c := range g.Connectors {
		if c.ID == model.ConnectorHTTP {
			t.Fatalf("a product without HTTP has an http connector: %+v", c)
		}
	}
	proc := containerOf(g.Architecture, model.ContainerProcess)
	if proc == nil {
		t.Fatal("no process container")
	}
	if strings.Contains(proc.Technology, "net/http") {
		t.Errorf("the process is drawn as an HTTP server: %q", proc.Technology)
	}
	if len(proc.Ports) != 1 {
		t.Fatalf("ports %+v, want the framework's one", proc.Ports)
	}
	port := proc.Ports[0]
	if port.Owner != model.PortOwnerApp || port.Address != strings.TrimPrefix(a.URL(), "http://") ||
		slices.Contains(port.Serves, model.ServesHTTP) || !slices.Contains(port.Serves, model.ServesHealth) {
		t.Errorf("the framework's port %+v", port)
	}
}

// Memory-only data: one store kept in memory whatever the data directory.
var Cache = app.NewService("cache", "A store kept in memory, for the connectors' tests.")

type cached struct {
	Key string `json:"key"`
}

var _ = Cache.Store("entries", func(c cached) string { return c.Key }, app.InMemory())

// The links to the data directory and to memory each carry exactly the
// connectors of the nodes they hold: the resource is not the connector.
func TestLinksCarryTheirConnectors(t *testing.T) {
	needsFileStore(t)
	a := app.NewApp("web", Web, Shop, Audit, Cache).With(app.DataDir(t.TempDir()), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer a.Stop(context.Background())
	arch := a.Graph().Architecture
	volume := linkOf(arch, "container:process", "container:volume")
	if volume == nil || !slices.Equal(volume.Connectors, []string{model.ConnectorMail, model.ConnectorQueue, model.ConnectorStore}) {
		t.Errorf("process → data directory carries %+v", volume)
	}
	if vol := containerOf(arch, model.ContainerVolume); vol == nil || !slices.Equal(vol.Settings, []string{"WEB_DATA_DIR"}) {
		t.Errorf("the data directory owns WEB_DATA_DIR: %+v", vol)
	}
	memory := linkOf(arch, "container:process", "container:memory")
	if memory == nil || !slices.Equal(memory.Connectors, []string{model.ConnectorStore}) || !slices.Equal(memory.Nodes, []string{"cache/store/entries"}) {
		t.Errorf("process → memory carries %+v", memory)
	}
	if l := linkOf(arch, "system:clients", "container:process"); l == nil || !slices.Equal(l.Connectors, []string{model.ConnectorHTTP}) {
		t.Errorf("clients → process carries %+v", l)
	}
}

// A malformed SMTP URL refuses the start, and the password in it reaches
// nothing: the start error, the diagnostics, the boot steps, the graph and
// the logs.
func TestAMalformedSMTPURLLeaksNothing(t *testing.T) {
	const sentinel = "s3ntinel-pw-7c1d"
	var logs syncBuffer
	t.Setenv("MAIL_SMTP_URL", "smtp://postmaster:"+sentinel+"@relay.example:notaport")
	a := app.NewApp("mail", Members).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(&logs))
	err := a.Start(t.Context())
	if err == nil {
		if err := a.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("a malformed SMTP URL started")
	}
	raw, rawErr := json.Marshal(a.Graph())
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	for where, text := range map[string]string{"the start error": err.Error(), "the graph": string(raw), "the logs": logs.String()} {
		if strings.Contains(text, sentinel) || strings.Contains(text, "postmaster") {
			t.Errorf("%s discloses the credentials", where)
		}
	}
}
