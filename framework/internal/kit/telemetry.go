package kit

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/framework/telemetry"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// Telemetry: a server or a daemon reports every span and every phase change
// on framework/telemetry's port (ADR 0149) — a no-op unless the app was
// started with an exporter, which only EMITS, on a private socket a tool
// attaches to (`kit attach`). It is configured at the start and never after:
// KIT_TELEMETRY names the socket ("on" is <runtime dir>/telemetry.sock, or
// <runtime dir of the binary>/<app>.telemetry.sock for a role of one),
// KIT_TELEMETRY_GIDS the groups admitted besides the product's account — the
// on-call group a deployment declares. [Telemetry] does the same in code. A
// CLI run never exports.

// Telemetry exports the app's telemetry on the private socket at path —
// absolute, and short enough for a Unix socket (pkg/v1/proc/ipc) — admitting the groups gids besides the
// product's own account. It wins over KIT_TELEMETRY.
func Telemetry(path string, gids ...int) AppConfigurer {
	return appOption(func(o *appOptions) { o.telemetry, o.telemetryGIDs = &path, gids })
}

// DesignDigest records the digest of the design the product's code was
// generated from — what the generated wiring passes —, so the telemetry
// handshake can say it and a tool attached to the product can tell a process
// built from another design.
func DesignDigest(digest string) AppConfigurer {
	return appOption(func(o *appOptions) { o.digest = digest })
}

// telemetrySocket is where KIT_TELEMETRY=on puts the socket.
func (a *App) telemetrySocket() string {
	if a.binary != nil {
		// Two roles of a binary share its directory: each its own socket.
		return filepath.Join(ipc.RuntimeDir(a.product()), a.name+".telemetry.sock")
	}
	return filepath.Join(ipc.RuntimeDir(a.product()), "telemetry.sock")
}

// telemetryConfig is where the exporter listens and whom it admits; "" when
// the app exports nothing.
func (a *App) telemetryConfig() (path string, gids []int, err error) {
	path = os.Getenv("KIT_TELEMETRY")
	if a.opts.telemetry != nil {
		path, gids = *a.opts.telemetry, a.opts.telemetryGIDs
	} else if raw := os.Getenv("KIT_TELEMETRY_GIDS"); raw != "" {
		for f := range strings.SplitSeq(raw, ",") {
			gid, convErr := strconv.Atoi(strings.TrimSpace(f))
			if convErr != nil || gid < 0 {
				return "", nil, failure(CodeAppConfig, "TELEMETRY_INVALID", "KIT_TELEMETRY_GIDS is a comma-separated list of group IDs", nil)
			}
			gids = append(gids, gid)
		}
	}
	switch path {
	case "", "off":
		return "", nil, nil
	case "on":
		path = a.telemetrySocket()
	}
	if !filepath.IsAbs(path) {
		return "", nil, failure(CodeAppConfig, "TELEMETRY_INVALID", "KIT_TELEMETRY is on, off or an absolute socket path", nil)
	}
	return path, gids, nil
}

// startTelemetry builds the exporter over the graph's node IDs and opens its
// socket; the spans and the phases report on it from then on.
func (a *App) startTelemetry(ctx context.Context) error {
	path, gids, err := a.telemetryConfig()
	if err != nil || path == "" {
		return err
	}
	g := a.Graph()
	nodes := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		if _, err := model.ParseID(n.ID); err == nil {
			nodes = append(nodes, n.ID)
		}
	}
	hello := telemetry.HelloValue{Product: a.name, Digest: a.opts.digest, Nodes: nodes}
	if a.binary != nil {
		hello.Binary, hello.Role = model.BinaryID(a.binary.name), model.RoleID(a.binary.name, a.role)
	}
	if a.build != nil {
		hello.Revision = a.build.Product.Revision
	}
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: path, AllowGIDs: gids, Hello: hello})
	if err != nil {
		return err
	}
	if err := ex.Start(ctx); err != nil {
		return err
	}
	a.tel.Store(ex)
	return nil
}

// stopTelemetry stops the exporter, if one runs, and lets producers emit
// to nothing again.
func (a *App) stopTelemetry(ctx context.Context) error {
	ex := a.tel.Swap(nil)
	if ex == nil {
		return nil
	}
	return ex.Stop(ctx)
}

// report puts a span that ended on the telemetry port: numbers only — the
// node and the node it came from as references, the operation, the outcome,
// the code of its error. Without an exporter it costs one atomic load.
func (a *App) report(sp *span, err error) {
	ex := a.tel.Load()
	if ex == nil {
		return
	}
	ev := telemetry.Event{
		Kind: telemetry.KindSpan, Op: telemetry.OpOf(sp.s.Op), Outcome: telemetry.OutcomeOK,
		Node: ex.Ref(sp.s.Node), From: ex.Ref(sp.s.From), Start: sp.start.UnixNano(), Duration: int64(a.clock.Since(sp.start)),
	}
	if err != nil {
		ev.Outcome = telemetry.OutcomeError
		if code, ok := errs.CodeOf(err); ok {
			ev.Code = uint32(code)
		}
	}
	if sp.sdk != nil {
		sc := sp.sdk.SpanContext()
		ev.TraceID, ev.SpanID = sc.TraceID, sc.SpanID
	}
	ex.Emit(&ev)
}

// reportPhase puts a phase change on the telemetry port.
func (a *App) reportPhase(phase string) {
	if ex := a.tel.Load(); ex != nil {
		ex.Emit(&telemetry.Event{
			Kind: telemetry.KindPhase, Op: telemetry.OpOf(phase), Outcome: telemetry.OutcomeOK,
			Start: a.clock.Now().UnixNano(),
		})
	}
}
