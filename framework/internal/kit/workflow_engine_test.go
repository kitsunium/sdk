package kit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The engine keeps a workflow's bookkeeping in the file kit always wrote.
// After a restart an instance keeps the instant it entered its state — its
// timers keep counting — and its history.
func TestAWorkflowKeepsItsInstancesAcrossARestart(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	clk := clock.NewManualClock(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC))
	run := func(during func(app *kit.App)) {
		t.Helper()
		app := kit.NewApp("shop", Shop, Audit).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev),
			kit.Analyze(false), kit.Logs(io.Discard), kit.Clock(clk))
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		during(app)
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	run(func(app *kit.App) {
		id = create(t, app, "kept", 3).ID
		clk.Advance(time.Minute)
		if r := call(t, app, "POST /items/"+id+"/publish", noBody); r.status != http.StatusOK {
			t.Fatalf("publish: %d %s", r.status, r.body)
		}
	})

	raw, rawErr := os.ReadFile(filepath.Join(dir, "shop", "lifecycle.workflow.json"))
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	var file map[string]model.Instance
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the workflow's file: %v\n%s", err, raw)
	}
	published := time.Date(2026, 9, 26, 9, 1, 0, 0, time.UTC)
	inst := file[id]
	if inst.ID != id || inst.State != "live" || !inst.EnteredAt.Equal(published) || len(inst.History) != 2 {
		t.Fatalf("the file keeps %+v", inst)
	}
	if h := inst.History[0]; h.Event != "create" || h.From != "" || h.To != "draft" || h.Trigger != model.TriggerCreate {
		t.Errorf("the creation step %+v", h)
	}
	if h := inst.History[1]; h.Event != "publish" || h.From != "draft" || h.To != "live" || h.Trigger != model.TriggerEvent || h.Caller != "shop/endpoint/FireItem" {
		t.Errorf("the publication step %+v", h)
	}

	clk.Advance(time.Hour / 2)
	run(func(app *kit.App) {
		r := call(t, app, "GET /_kit/api/instances?workflow=shop/workflow/lifecycle", noBody)
		var list []model.Instance
		r.json(t, &list)
		for _, got := range list {
			if got.ID != id {
				continue
			}
			if got.State != "live" || !got.EnteredAt.Equal(published) || len(got.History) != 2 {
				t.Errorf("after a restart the instance is %+v", got)
			}
			return
		}
		t.Fatalf("the instance is gone after a restart: %s", r.body)
	})
}
