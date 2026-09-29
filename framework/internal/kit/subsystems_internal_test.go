package kit

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"
)

// A YAML configuration file read by a program that did not import
// framework/kit/config/yaml refuses the start, naming the import — kit reads
// JSON only by itself.
func TestAFileInAFormatNotImportedIsRefused(t *testing.T) {
	format, _ := configFormats.Load("yaml")
	configFormats.Delete("yaml")
	t.Cleanup(func() { configFormats.Store("yaml", format) })
	svc := NewService("news", "News.")
	svc.Setting("page-size", 10)
	app := NewApp("news", svc).With(InMemory(), Logs(io.Discard),
		ConfigFiles(fstest.MapFS{"config/config.yaml": {Data: []byte("page-size: 3\n")}}))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	var said strings.Builder
	for _, d := range de.Diagnostics {
		said.WriteString(d.Message)
	}
	if !strings.Contains(said.String(), "framework/kit/config/yaml") {
		t.Errorf("the refusal does not name the import: %s", said.String())
	}
}

// A server whose program did not import framework/kit/server is refused at
// the start, naming the import: it would run and answer nothing. A daemon
// needs no such import.
func TestAServerWithoutItsPackageIsRefused(t *testing.T) {
	start := httpStart.Load()
	httpStart.Store(nil)
	t.Cleanup(func() { httpStart.Store(start) })
	app := NewApp("web", NewService("web", "Serves.")).With(InMemory(), Logs(io.Discard))
	var de *DiagnosticsError
	if err := app.Start(context.Background()); !errors.As(err, &de) || !strings.Contains(de.Error(), "framework/kit/server") {
		t.Fatalf("a server without framework/kit/server: %v", err)
	}
	daemon := NewApp("d", NewService("d", "Runs.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	if err := daemon.Start(context.Background()); err != nil {
		t.Fatalf("a daemon: %v", err)
	}
	if err := daemon.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}
