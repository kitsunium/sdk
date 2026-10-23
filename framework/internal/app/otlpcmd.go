package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// otlpUsageText is `otlp`'s usage; %[1]s is the product's name.
const otlpUsageText string = `usage:
  %[1]s otlp dead-letters [traces|metrics]
      the batches the collector refused for good, by signal: their ID, when
      the last attempt failed, the failure's code, reason and public text,
      their attempts and their size — never their payload
  %[1]s otlp replay [traces|metrics] [--drop] [ID...]
      move the dead letters named — every one when none is — back into the
      live queue, their attempts counted afresh: the export sends them
      again, a running product as soon as it next polls; --drop deletes them
      for good instead
The queues are read where the export keeps them: under the data directory,
or on the database otlp-database names. An export that keeps them in memory
has none to read.
`

// The otlp command (issue #536): the OTLP export's dead letters, read,
// replayed or dropped by an operator — never through a route (D13). A batch
// the collector refused for good (a 400, or a 4xx other than 408 and 429 of
// the product's own sender) is dead-lettered in its signal's queue; this
// command opens that queue as the export opens it — the same directory or
// table, the same policy — through the subsystem (plug.OTLPDriverConfig's
// Queue), so a product that does not import framework/app/otlp, or declares
// no export, has no such command and links none of it. A queue is shared
// by processes as the SDK's queues are, without a lock: a replay while the
// product runs is sent by its export at its next poll, which counts it
// pending again from its next start.

// otlpMaxDeadLetters bounds the dead letters the command reads per signal.
const otlpMaxDeadLetters int = 1000

// otlpArgs are `otlp`'s arguments, read: the verb, the signals it acts on,
// whether a replay drops, and the dead letters it names.
type otlpArgs struct {
	verb    string
	signals []string
	drop    bool
	ids     []string
}

// otlpCommandOf is Main's `otlp` command of app a.
func otlpCommandOf(a *App) func(ctx context.Context, args []string) int {
	return func(ctx context.Context, args []string) int { return a.otlpCommand(ctx, args, os.Stdout, os.Stderr) }
}

// otlpCommand runs `otlp …`.
func (a *App) otlpCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	parsed, ok := parseOTLPArgs(args)
	if !ok || a.opts.otlp == nil {
		fmt.Fprintf(stderr, otlpUsageText, a.name)
		return 2
	}
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if a.otlpSettingsRefused(stderr) {
		return exitConfig
	}
	err := a.withOTLPQueues(ctx, func(cfg *plug.OTLPConfig) error {
		if parsed.verb == "dead-letters" {
			return a.listOTLPDeadLetters(ctx, cfg, parsed.signals, stdout)
		}
		return a.replayOTLPDeadLetters(ctx, cfg, &parsed, stdout, stderr)
	})
	switch {
	case err == nil:
		return 0
	case errs.PublicOf(err) != "":
		fmt.Fprintln(stderr, errs.PublicOf(err))
	default:
		fmt.Fprintln(stderr, describeText(err))
	}
	return 1
}

// parseOTLPArgs reads `otlp`'s arguments: dead-letters and a signal at most,
// or replay, a signal, --drop and the IDs in any order; ok is false for any
// other.
func parseOTLPArgs(args []string) (otlpArgs, bool) {
	if len(args) == 0 || !slices.Contains([]string{"dead-letters", "replay"}, args[0]) {
		return otlpArgs{}, false
	}
	out := otlpArgs{verb: args[0], signals: []string{OTLPTraces, OTLPMetrics}}
	rest := args[1:]
	if len(rest) > 0 && (rest[0] == OTLPTraces || rest[0] == OTLPMetrics) {
		out.signals, rest = rest[:1], rest[1:]
	}
	if out.verb == "dead-letters" && len(rest) > 0 {
		return otlpArgs{}, false
	}
	for _, arg := range rest {
		if !out.take(arg) {
			return otlpArgs{}, false
		}
	}
	return out, true
}

// take reads one of replay's arguments after its signal: --drop, or an ID;
// false for any other flag.
func (p *otlpArgs) take(arg string) bool {
	switch {
	case arg == "--drop":
		p.drop = true
	case arg != "" && !strings.HasPrefix(arg, "-"):
		p.ids = append(p.ids, arg)
	default:
		return false
	}
	return true
}

// otlpSettingsRefused resolves the settings — the export's among them — and
// says every error the start would refuse them for.
func (a *App) otlpSettingsRefused(stderr io.Writer) bool {
	resolved := a.resolveSettings()
	a.settingValues.Store(&resolved.values)
	refused := false
	for _, d := range slices.Concat(resolved.problems, a.otlpProblems()) {
		if d.Severity == "error" {
			fmt.Fprintf(stderr, "error: %s\n", d.Message)
			refused = true
		}
	}
	return refused
}

// withOTLPQueues runs fn with the export's queues placed as its start
// places them: on the database otlp-database names, opened for the
// command, else under the data directory; refused when they would be in
// memory.
func (a *App) withOTLPQueues(ctx context.Context, fn func(cfg *plug.OTLPConfig) error) error {
	e := a.opts.otlp
	cfg := &plug.OTLPConfig{
		Clock: pluggedClock{Timed: a.clock}, Service: a.name, Version: frameworkVersion(),
		WhenFull: resolvedSetting(a, e.whenFull), MaxBytes: int64(resolvedSetting(a, e.maxBytes)),
		MaxBatches: resolvedSetting(a, e.maxBatches), MaxAge: resolvedSetting(a, e.maxAge),
		SendTimeout: resolvedSetting(a, e.sendTimeout), Interval: resolvedSetting(a, e.interval),
		DrainTimeout: resolvedSetting(a, e.drain), SettleTimeout: resolvedSetting(a, e.settle),
	}
	if a.opts.memory {
		return otlpInMemory()
	}
	if resolvedSetting(a, e.database) != "" {
		// The databases are the store subsystem's, which a product that
		// declares one imports: opened through its hook, so the export's
		// subsystem links none of their code.
		h := stores()
		if h == nil {
			return storesMissing()
		}
		closeAll, err := h.openDatabases(a, ctx)
		if err != nil {
			return err
		}
		return errors.Join(a.onOTLPDatabase(ctx, cfg, fn), closeAll())
	}
	if a.dataDir == "" {
		return otlpInMemory()
	}
	cfg.Dir = filepath.Join(a.dataDir, otlpDataDir)
	return fn(cfg)
}

// onOTLPDatabase runs fn with the export's queues on the database
// otlp-database names, open; refused when it is not.
func (a *App) onOTLPDatabase(ctx context.Context, cfg *plug.OTLPConfig, fn func(cfg *plug.OTLPConfig) error) error {
	if err := a.otlpOnDatabase(ctx, cfg); err != nil {
		return err
	}
	if cfg.Database == nil {
		return otlpInMemory()
	}
	return fn(cfg)
}

// otlpInMemory is the refusal of a command on queues kept in memory.
func otlpInMemory() error {
	return failure(CodeOTLPExport, "OTLP_IN_MEMORY",
		"the OTLP export keeps its queues in memory: no dead letter outlives its process; set <PRODUCT>_DATA_DIR or otlp-database", nil)
}

// withOTLPQueue runs fn on signal's queue, opened for the command and
// closed after it.
func withOTLPQueue(ctx context.Context, cfg *plug.OTLPConfig, signal string, fn func(q plug.OTLPQueue) error) (err error) {
	q, err := plug.OTLP.Load().Queue(ctx, cfg, signal)
	if err != nil {
		return failure(CodeOTLPExport, "OTLP_QUEUE", "the OTLP export's queue cannot be opened", err, errs.String("signal", signal))
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	return fn(q)
}

// listOTLPDeadLetters writes the dead letters of signals on stdout, one a
// line, never a payload.
func (a *App) listOTLPDeadLetters(ctx context.Context, cfg *plug.OTLPConfig, signals []string, stdout io.Writer) error {
	n := 0
	for _, signal := range signals {
		err := withOTLPQueue(ctx, cfg, signal, func(q plug.OTLPQueue) error {
			dead, err := q.DeadLetters(ctx, otlpMaxDeadLetters)
			for _, d := range dead {
				fmt.Fprintln(stdout, otlpDeadLetterLine(signal, &d))
			}
			n += len(dead)
			return err
		})
		if err != nil {
			return err
		}
	}
	if n == 0 {
		fmt.Fprintln(stdout, "no dead letter in the OTLP export's queues: "+strings.Join(signals, ", "))
	}
	return nil
}

// otlpDeadLetterLine is one dead letter as the command lists it: its signal,
// its ID, when it failed, its code, reason and attempts, its size, the
// failure's public text, and the codes it gathered as it was wrapped (#500).
func otlpDeadLetterLine(signal string, d *plug.OTLPDeadLetterValue) string {
	code := "-"
	if d.Code != 0 {
		code = errs.Code(d.Code).String()
	}
	line := signal + "\t" + d.ID + "\t" + d.FailedAt.UTC().Format(time.RFC3339) + "\t" + code + "\t" + d.Reason +
		"\t" + strconv.Itoa(d.Deliveries) + " attempts\t" + strconv.Itoa(d.Size) + " bytes"
	if d.Cause != "" {
		line += "\t" + d.Cause
	}
	if len(d.Trail) > 0 {
		trail := make([]string, 0, len(d.Trail))
		for _, c := range d.Trail {
			trail = append(trail, errs.Code(c).String())
		}
		line += "\ttrail " + strings.Join(trail, " <- ")
	}
	return line
}

// replayOTLPDeadLetters replays, or drops, the dead letters p names — every
// one of its signals' when it names none —, and says how many on stdout; a
// named one no queue holds is said on stderr and fails the command once the
// others are done.
func (a *App) replayOTLPDeadLetters(ctx context.Context, cfg *plug.OTLPConfig, p *otlpArgs, stdout, stderr io.Writer) error {
	done := map[string]bool{}
	verb := "replayed"
	if p.drop {
		verb = "dropped"
	}
	for _, signal := range p.signals {
		n := 0
		err := withOTLPQueue(ctx, cfg, signal, func(q plug.OTLPQueue) (err error) {
			n, err = otlpActOn(ctx, q, p, done)
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %d dead letter(s) of %s\n", verb, n, signal)
	}
	var missing []string
	for _, id := range p.ids {
		if !done[id] {
			fmt.Fprintln(stderr, "no dead letter "+clip(id)+" in the OTLP export's queues")
			missing = append(missing, clip(id))
		}
	}
	if len(missing) > 0 {
		return failure(CodeOTLPExport, "OTLP_DEAD_LETTER_UNKNOWN", "no such dead letter", nil, errs.String("ids", strings.Join(missing, ",")))
	}
	return nil
}

// otlpActOn replays, or drops, in q the dead letters p names that are not
// done yet — every one q holds when p names none —, marks each one done,
// and returns how many it acted on.
func otlpActOn(ctx context.Context, q plug.OTLPQueue, p *otlpArgs, done map[string]bool) (int, error) {
	ids, err := otlpIDs(ctx, q, p.ids, done)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		found, err := otlpAct(ctx, q, id, p.drop)
		if err != nil {
			return n, err
		}
		if found {
			done[id] = true
			n++
		}
	}
	return n, nil
}

// otlpIDs are the dead letters to act on in q: those named and not done
// yet, or every one q holds when none is named.
func otlpIDs(ctx context.Context, q plug.OTLPQueue, named []string, done map[string]bool) ([]string, error) {
	if len(named) > 0 {
		var out []string
		for _, id := range named {
			if !done[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}
	dead, err := q.DeadLetters(ctx, otlpMaxDeadLetters)
	out := make([]string, 0, len(dead))
	for _, d := range dead {
		out = append(out, d.ID)
	}
	return out, err
}

// otlpAct replays the dead letter id of q, or drops it.
func otlpAct(ctx context.Context, q plug.OTLPQueue, id string, drop bool) (bool, error) {
	if drop {
		return q.Drop(ctx, id)
	}
	return q.Replay(ctx, id)
}
