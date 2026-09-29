//go:build unix

package kit_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/telemetry"
	"github.com/kitsunium/sdk/pkg/v1/ipc"
)

// A daemon started with an exporter reports each connection its listener
// served as a span record: the listener's reference from the handshake's
// table, the connect operation, the outcome — and nothing a person could be
// identified by, because the record has no field for it.
func TestADaemonReportsItsSpansOnTheTelemetrySocket(t *testing.T) {
	dir := shortDir(t)
	telPath := filepath.Join(dir, "run", "t.sock")
	app, l := daemonApp(t, filepath.Join(dir, "run", "d.sock"), kit.Telemetry(telPath))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	tc, tcErr := ipc.Dial(t.Context(), ipc.Config{Path: telPath})
	if tcErr != nil {
		t.Fatal(tcErr)
	}
	defer tc.Close()
	r := bufio.NewReader(tc)
	line, lineErr := r.ReadBytes('\n')
	if lineErr != nil {
		t.Fatal(lineErr)
	}
	var h struct {
		Product string
		Nodes   []string
	}
	if err := json.Unmarshal(line, &h); err != nil || h.Product != "statusline" {
		t.Fatalf("the handshake: %s %v", line, err)
	}
	ref := slices.Index(h.Nodes, "render/listener/socket") + 1
	if ref == 0 {
		t.Fatalf("the node table lacks the listener: %v", h.Nodes)
	}
	time.Sleep(20 * time.Millisecond)
	c, cErr := l.Dial(t.Context())
	if cErr != nil {
		t.Fatal(cErr)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, telemetry.RecordSize)
	if err := tc.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			t.Fatalf("no record of the connection: %v", err)
		}
		ev, _, ok := telemetry.Decode(b)
		if ok && ev.Kind == telemetry.KindSpan && ev.Op == telemetry.OpConnect {
			if int(ev.Node) != ref || ev.Outcome != telemetry.OutcomeOK {
				t.Errorf("the connection's record: %+v", ev)
			}
			return
		}
	}
}
