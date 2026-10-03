//go:build unix

package telemetry_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/framework/telemetry"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("removing %s: %v", dir, err)
		}
	})
	return filepath.Join(dir, "run", "t.sock")
}

func hello() telemetry.HelloValue {
	return telemetry.HelloValue{
		Product: "todo", Binary: "todo", Role: "serve", Revision: "abc123", Digest: "d1",
		Nodes: []string{"todos", "todos/endpoint/List", "todos/store/todos"},
	}
}

// A client reads the handshake, then the records of what was emitted after
// it attached, in order, with nothing but numbers in them.
func TestAnAttachedClientReadsTheHandshakeThenTheRecords(t *testing.T) {
	path := socketPath(t)
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: path, Hello: hello()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ex.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()
	c, err := ipc.Dial(t.Context(), ipc.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Protocol, Product, Revision, Instance string
		ModelVersion, RecordSize              int
	}
	if err := json.Unmarshal(line, &h); err != nil {
		t.Fatal(err)
	}
	if h.Protocol != telemetry.Protocol || h.Product != "todo" || h.Revision != "abc123" ||
		h.ModelVersion != model.Version || h.RecordSize != telemetry.RecordSize || h.Instance != ex.Instance() {
		t.Fatalf("the handshake: %s", line)
	}
	list := ex.Ref("todos/endpoint/List")
	if list != 2 || ex.Ref("nope") != 0 {
		t.Fatalf("the references: %d %d", list, ex.Ref("nope"))
	}
	// The client is registered once the handshake is written; give the
	// accept loop the lock back before emitting.
	time.Sleep(20 * time.Millisecond)
	for i := range 3 {
		ex.Emit(&telemetry.Event{
			Kind: telemetry.KindSpan, Op: telemetry.OpRequest, Outcome: telemetry.OutcomeOK,
			Node: list, Start: int64(1000 + i), Duration: 5, TraceID: [16]byte{1}, SpanID: [8]byte{byte(i + 1)},
		})
	}
	var last uint64
	for i := range 3 {
		b := make([]byte, telemetry.RecordSize)
		if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(r, b); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		ev, seq, ok := telemetry.Decode(b)
		if !ok || ev.Node != list || ev.Start != int64(1000+i) || ev.SpanID[0] != byte(i+1) || ev.Op != telemetry.OpRequest {
			t.Fatalf("record %d: %+v ok=%v", i, ev, ok)
		}
		if seq <= last {
			t.Fatalf("the sequence went back: %d after %d", seq, last)
		}
		last = seq
	}
}

// Without a client the ring is drained all the same; a full ring drops and
// counts, and the producer never waits.
func TestAFullRingDropsAndCounts(t *testing.T) {
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: socketPath(t), Buffer: telemetry.MinBuffer, Hello: hello()})
	if err != nil {
		t.Fatal(err)
	}
	ev := telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpCall, Node: 1}
	for range 200 {
		ex.Emit(&ev)
	}
	want := uint64(200 - telemetry.MinBuffer)
	if got := ex.Dropped(); got != want {
		t.Errorf("dropped %d, want %d", got, want)
	}
}

// An exporter that cannot be honoured is refused before anything is opened.
func TestAnExporterIsCheckedFirst(t *testing.T) {
	for _, cfg := range []telemetry.ExporterConfig{
		{},
		{Path: "/tmp/x.sock", Buffer: 3},
		{Path: "/tmp/x.sock", Hello: telemetry.HelloValue{Nodes: []string{"Not An ID"}}},
	} {
		if _, err := telemetry.NewExporter(&cfg); !errs.HasCode(err, telemetry.CodeMisconfigured) {
			t.Errorf("NewExporter(%+v) = %v", cfg, err)
		}
	}
}

// The operations are framework/model's, by name, both ways.
func TestTheOperationsAreTheModels(t *testing.T) {
	for _, name := range []string{
		model.OpRequest, model.OpCall, model.OpRead, model.OpWrite, model.OpPublish, model.OpDeliver,
		model.OpTransition, model.OpRun, model.OpAuth, model.OpSend, model.OpDeliverMail, model.OpSecret, model.OpDispatch,
		model.OpAsk, model.OpHandle, model.OpConnect, model.OpCLI,
		model.PhaseStarting, model.PhaseServing, model.PhaseDraining, model.PhaseStopped, model.PhaseFailed,
	} {
		if op := telemetry.OpOf(name); op == telemetry.OpNone || telemetry.OpNames[op] != name {
			t.Errorf("OpOf(%q) = %d", name, op)
		}
	}
	if telemetry.OpOf("") != telemetry.OpNone || telemetry.OpOf("nope") != telemetry.OpNone {
		t.Error("an unknown name has an op")
	}
}
