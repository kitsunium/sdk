package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// queueUsageText is `queue`'s usage; %[1]s is the product's name.
const queueUsageText string = `usage:
  %[1]s queue dead-letters [NODE]
      the messages a consumer gave up on — a subscription's, a watch's, a
      queued command's, a mailer's outbox — every consumer's, or NODE's:
      their consumer, their ID, when the last attempt failed, the failure's
      code, reason and public text, their attempts and their size — never
      their payload
  %[1]s queue replay NODE ID...
      move the dead letters named back into NODE's live queue, their attempts
      counted afresh: its consumer handles them again, a running product's
      at its next poll, a stopped one's at its next start
  %[1]s queue drop NODE ID...
      delete the dead letters named for good
The queues are read where the start keeps them: on the database that keeps
the consumer, or under the data directory. A product that keeps its data in
memory has none to read.
`

// The queue command (issue #1138): a consumer's dead letters, read, replayed
// or dropped by an operator — never through a route (D13) —, as `otlp` does
// for the export's (#536). Every consumer the app mounts with a queue — a
// subscription, a watch, a queued command, a mailer's outbox — is opened as
// the start opens it: its table on the database that keeps it, opened for the
// command through the store subsystem's hook, else its file queue under the
// data directory. The SDK's queues are shared by processes without a lock,
// so the command runs beside a running product as `otlp` does: a replayed
// message is handled at the consumer's next poll.

// queueMaxDeadLetters bounds the dead letters the command reads per consumer.
const queueMaxDeadLetters int = 1000

// queueVerbs are `queue`'s verbs.
var queueVerbs = []string{"dead-letters", "replay", "drop"}

// queueArgs are `queue`'s arguments, read: the verb, the consumer it names —
// "" for every one —, and the dead letters it names.
type queueArgs struct {
	verb, node string
	ids        []string
}

// queueCLI is Main's `queue` command, nil in a program that does not import
// the queue subsystem: it has no consumer's queue to read.
func (a *App) queueCLI() func(ctx context.Context, args []string) int {
	if !queuesLinked() {
		return nil
	}
	return func(ctx context.Context, args []string) int { return a.queueCommand(ctx, args, os.Stdout, os.Stderr) }
}

// queueCommand runs `queue …`.
func (a *App) queueCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	parsed, ok := parseQueueArgs(args)
	if !ok {
		fmt.Fprintf(stderr, queueUsageText, a.name)
		return 2
	}
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if a.queueSettingsRefused(stderr) {
		return exitConfig
	}
	if err := a.mount(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.unmount()
	owners, known := a.queueOwnersNamed(parsed.node)
	if !known {
		a.sayQueueOwners(stderr, parsed.node)
		return 2
	}
	err := a.withConsumerQueues(ctx, owners, func(queues []consumerQueue) error {
		if parsed.verb == "dead-letters" {
			return listQueueDeadLetters(ctx, queues, stdout)
		}
		return actOnQueueDeadLetters(ctx, queues[0], &parsed, stdout, stderr)
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

// parseQueueArgs reads `queue`'s arguments: dead-letters and a consumer at
// most, or replay or drop, a consumer and one ID or more; ok is false for any
// other.
func parseQueueArgs(args []string) (queueArgs, bool) {
	if len(args) == 0 || !slices.Contains(queueVerbs, args[0]) {
		return queueArgs{}, false
	}
	out := queueArgs{verb: args[0]}
	rest := args[1:]
	for _, arg := range rest {
		if arg == "" || strings.HasPrefix(arg, "-") {
			return queueArgs{}, false
		}
	}
	if out.verb == "dead-letters" {
		if len(rest) > 1 {
			return queueArgs{}, false
		}
		if len(rest) == 1 {
			out.node = rest[0]
		}
		return out, true
	}
	if len(rest) < 2 {
		return queueArgs{}, false
	}
	out.node, out.ids = rest[0], rest[1:]
	return out, true
}

// queueSettingsRefused resolves the settings — the databases' among them —
// and says every error the start would refuse them for.
func (a *App) queueSettingsRefused(stderr io.Writer) bool {
	resolved := a.resolveSettings()
	a.settingValues.Store(&resolved.values)
	refused := false
	for _, d := range resolved.problems {
		if d.Severity == "error" {
			fmt.Fprintf(stderr, "error: %s\n", d.Message)
			refused = true
		}
	}
	return refused
}

// queueOwners are the consumers the app mounts that keep a queue, by ID.
func (a *App) queueOwners() []queueOwner {
	var out []queueOwner
	for _, n := range a.mountedNodes() {
		if q, ok := n.(queueOwner); ok && q.queueUnder() != "" {
			out = append(out, q)
		}
	}
	slices.SortFunc(out, func(x, y queueOwner) int { return strings.Compare(x.base().id, y.base().id) })
	return out
}

// queueOwnersNamed are the consumers the command acts on: every one when
// node is "", else the one whose ID is node; known is false when none is.
func (a *App) queueOwnersNamed(node string) (owners []queueOwner, known bool) {
	all := a.queueOwners()
	if node == "" {
		return all, true
	}
	for _, q := range all {
		if q.base().id == node {
			return []queueOwner{q}, true
		}
	}
	return nil, false
}

// sayQueueOwners says on stderr that node is no consumer, and which ones the
// product mounts.
func (a *App) sayQueueOwners(stderr io.Writer, node string) {
	ids := make([]string, 0, len(a.queueOwners()))
	for _, q := range a.queueOwners() {
		ids = append(ids, q.base().id)
	}
	if len(ids) == 0 {
		fmt.Fprintln(stderr, say("queue.no-consumer", "node", clip(node)))
		return
	}
	fmt.Fprintln(stderr, say("queue.unknown-consumer", "node", clip(node), "consumers", strings.Join(ids, ", ")))
}

// consumerQueue is one consumer's queue as the command opened it: nil
// broker when the consumer has none yet — a file queue nothing ever wrote.
type consumerQueue struct {
	broker plug.Broker
	node   string
}

// withConsumerQueues runs fn with the queues of owners opened as the start
// opens them — the databases that keep some opened for the command —, and
// closes them after; refused when the product keeps its data in memory.
func (a *App) withConsumerQueues(ctx context.Context, owners []queueOwner, fn func([]consumerQueue) error) (err error) {
	if a.opts.memory || a.dataDir == "" {
		return failure(CodeQueueOpen, "QUEUE_IN_MEMORY",
			"the product keeps its queues in memory: no dead letter outlives its process; set <PRODUCT>_DATA_DIR", nil)
	}
	if a.anyQueueOnDatabase(owners) {
		h := stores()
		if h == nil {
			return storesMissing()
		}
		closeAll, openErr := h.openDatabases(a, ctx)
		if openErr != nil {
			return openErr
		}
		defer func() { err = errors.Join(err, closeAll()) }()
	}
	queues := make([]consumerQueue, 0, len(owners))
	defer func() {
		for _, q := range queues {
			err = errors.Join(err, closeQueue(q.broker))
		}
	}()
	for _, q := range owners {
		broker, openErr := a.openConsumerQueueForCommand(q)
		if openErr != nil {
			return openErr
		}
		queues = append(queues, consumerQueue{broker: broker, node: q.base().id})
	}
	return fn(queues)
}

// anyQueueOnDatabase reports whether a database keeps the queue of one of
// owners.
func (a *App) anyQueueOnDatabase(owners []queueOwner) bool {
	for _, q := range owners {
		if d := a.queueKeeper(q.base()); d != nil && a.queueKeptOn(q.base(), d) {
			return true
		}
	}
	return false
}

// openConsumerQueueForCommand opens the queue of the consumer q as its start
// opens it: its table on the database that keeps it, open; else its file
// queue under the data directory — nil, and nothing created, when that
// directory is not there. A mailer's outbox is opened under its own policy,
// and on a database without its sealing: a dead letter is replayed or
// dropped as it lies, and listed by the size it rests at.
func (a *App) openConsumerQueueForCommand(q queueOwner) (plug.Broker, error) {
	b, under := q.base(), q.queueUnder()
	cfg := plug.QueueConfig{Name: b.id, Policy: consumerPolicy(defaultMaxDeliveries)}
	if m, mailer := q.(*Mailer); mailer {
		cfg.Policy = m.outboxPolicy()
	}
	exists, err := a.placeConsumerQueue(&cfg, b, under)
	if err != nil || !exists {
		return nil, err
	}
	broker, err := a.openQueueWith(cfg)
	if err != nil {
		return nil, failure(CodeQueueOpen, "QUEUE_OPEN", "a consumer's queue cannot be opened", err, errs.String("node", b.id))
	}
	return broker, nil
}

// placeConsumerQueue places cfg, the queue of the consumer b kept under
// under, where the start places it: its table on the open database that
// keeps it, else its file queue under the data directory — exists false
// when that directory is not there, which nothing creates.
func (a *App) placeConsumerQueue(cfg *plug.QueueConfig, b *nodeBase, under string) (exists bool, err error) {
	if d := a.queueKeeper(b); d != nil && a.queueKeptOn(b, d) {
		r := a.runOf(d)
		if r == nil || !r.open() {
			return false, Unavailable(fmt.Sprintf("database %q is not open", d.name))
		}
		return true, sqlQueueConfig(cfg, b, r, queueTable(b, under))
	}
	dir := filepath.Join(a.dataDir, b.svc.name, under, b.name)
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return false, nil
	}
	cfg.Dir = dir
	return true, nil
}

// closeQueue releases what an opened queue holds, nothing for none.
func closeQueue(broker plug.Broker) error {
	if c, ok := broker.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// listQueueDeadLetters writes the dead letters of queues on stdout, one a
// line, never a payload.
func listQueueDeadLetters(ctx context.Context, queues []consumerQueue, stdout io.Writer) error {
	n := 0
	for _, q := range queues {
		if q.broker == nil {
			continue
		}
		dead, err := q.broker.DeadLetters(ctx, queueMaxDeadLetters)
		for i := range dead {
			d := &dead[i]
			fmt.Fprintln(stdout, deadLetterRow{
				where: q.node, id: d.Message.ID, failedAt: d.FailedAt, code: d.Code, reason: d.Reason,
				attempts: d.Deliveries, size: len(d.Message.Payload), cause: d.Cause, trail: d.Trail,
			}.line())
		}
		n += len(dead)
		if err != nil {
			return err
		}
	}
	if n == 0 {
		nodes := make([]string, 0, len(queues))
		for _, q := range queues {
			nodes = append(nodes, q.node)
		}
		fmt.Fprintln(stdout, say("queue.no-dead-letter", "consumers", strings.Join(nodes, ", ")))
	}
	return nil
}

// actOnQueueDeadLetters replays, or drops, the dead letters p names in q, and
// says how many on stdout; a named one the queue does not hold is said on
// stderr and fails the command once the others are done.
func actOnQueueDeadLetters(ctx context.Context, q consumerQueue, p *queueArgs, stdout, stderr io.Writer) error {
	manager, ok := q.broker.(plug.DeadLetterManager)
	n := 0
	var missing []string
	for _, id := range p.ids {
		found := false
		if ok {
			var err error
			found, err = actOnDeadLetter(ctx, manager, id, p.verb == "drop")
			if err != nil {
				return err
			}
		}
		if !found {
			fmt.Fprintln(stderr, say("queue.dead-letter-unknown", "id", clip(id), "node", q.node))
			missing = append(missing, clip(id))
			continue
		}
		n++
	}
	if p.verb == "drop" {
		fmt.Fprintln(stdout, say("queue.dropped", "count", n, "node", q.node))
	} else {
		fmt.Fprintln(stdout, say("queue.replayed", "count", n, "node", q.node))
	}
	if len(missing) > 0 {
		return failure(CodeQueueOpen, "QUEUE_DEAD_LETTER_UNKNOWN", "no such dead letter", nil,
			errs.String("node", q.node), errs.String("ids", strings.Join(missing, ",")))
	}
	return nil
}

// actOnDeadLetter replays the dead letter id of manager, or drops it.
func actOnDeadLetter(ctx context.Context, manager plug.DeadLetterManager, id string, drop bool) (bool, error) {
	if drop {
		return manager.Drop(ctx, id)
	}
	return manager.Replay(ctx, id)
}
