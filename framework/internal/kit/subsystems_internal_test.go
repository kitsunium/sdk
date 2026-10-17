package kit

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
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
	err := app.Start(context.Background())
	if !errors.As(err, &de) || !strings.Contains(de.Error(), "framework/kit/server") {
		t.Fatalf("a server without framework/kit/server: %v", err)
	}
	// A case of the refusal of ADR 0179 §6: the same code.
	if !errs.HasCode(err, CodeSubsystemMissing) {
		t.Errorf("a server without framework/kit/server is not refused with CodeSubsystemMissing: %v", err)
	}
	daemon := NewApp("d", NewService("d", "Runs.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	if err := daemon.Start(context.Background()); err != nil {
		t.Fatalf("a daemon: %v", err)
	}
	if err := daemon.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// withoutStores runs the test as a program that does not import
// framework/kit/store: the hook its package sets is nil (ADR 0179 §6), as
// the core sees it in such a program. The facade imports the package until
// ADR 0179's last step, so no product reaches the refusal yet but this way;
// the test binary plugs the subsystem in (subsystems_test.go), and the
// test takes it out until it ends.
func withoutStores(t *testing.T) {
	t.Helper()
	saved := plug.Stores.Load()
	plug.Stores.Store(nil)
	t.Cleanup(func() { plug.Stores.Store(saved) })
}

// probeOrder is the entity of the stores these tests declare.
type probeOrder struct{ ID string }

// A product that declares a store or a database without importing
// framework/kit/store is refused at the start (ADR 0179 §6): every
// declaration at once, each at its own position, naming the package to
// import, and the refusal carries CodeSubsystemMissing for errs.HasCode.
func TestADeclarationWithoutItsSubsystemIsRefused(t *testing.T) {
	withoutStores(t)
	svc := NewService("shop", "Sells.")
	svc.Store[probeOrder]("orders", func(o probeOrder) string { return o.ID })
	svc.Store[probeOrder]("returns", func(o probeOrder) string { return o.ID })
	app := NewApp("shop", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard),
		Database("main", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(svc)))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	var missing []model.Diagnostic
	for _, d := range de.Diagnostics {
		if strings.Contains(d.Message, storePackage) {
			missing = append(missing, d)
		}
	}
	// Two stores and a database: three declarations, refused together.
	if len(missing) != 3 {
		t.Fatalf("%d declarations refused for want of %s, want 3:\n%v", len(missing), storePackage, de)
	}
	for _, d := range missing {
		if d.Node == "" || d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "subsystems_internal_test.go") {
			t.Errorf("a refusal names no declaration at its position: %+v", d)
		}
		if d.Texts["fr"] == "" {
			t.Errorf("a refusal speaks no French: %+v", d)
		}
	}
	if !slices.Equal(de.Missing, []string{storePackage}) {
		t.Errorf("the refusal's Missing = %q, want [%s]", de.Missing, storePackage)
	}
	if !errs.HasCode(err, CodeSubsystemMissing) {
		t.Errorf("the refusal does not carry CodeSubsystemMissing: %v", err)
	}
	if got, want := errs.PublicOf(err), "a declaration needs a subsystem this program does not import"; got != want {
		t.Errorf("the refusal's public text is %q, want %q", got, want)
	}
	if c, _ := errs.CodeOf(err); c.String() != "0.4.2.83" {
		t.Errorf("CodeSubsystemMissing is %s, want 0.4.2.83", c)
	}
}

// With the store subsystem plugged in — framework/kit/store's, as the test
// binary has it —, the same product starts: the check refuses nothing a
// program imports.
func TestAStoreWithItsSubsystemIsNotRefused(t *testing.T) {
	svc := NewService("shop", "Sells.")
	svc.Store[probeOrder]("orders", func(o probeOrder) string { return o.ID })
	app := NewApp("shop", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("the start: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// A refusal of the declarations that misses no subsystem carries no
// CodeSubsystemMissing: the code says what the refusal is, not that there is
// one.
func TestARefusalThatMissesNoSubsystemCarriesNoCode(t *testing.T) {
	app := NewApp("twice", NewService("a", "A."), NewService("a", "A again.")).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	err := app.Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	if errs.HasCode(err, CodeSubsystemMissing) || de.Missing != nil {
		t.Errorf("a service declared twice is refused with CodeSubsystemMissing (Missing %q): %v", de.Missing, err)
	}
}
