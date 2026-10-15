package kit

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// watchUsageText is `watch`'s usage; %[1]s is the product's name.
const watchUsageText string = `usage:
  %[1]s watch replay WATCH
      queue one notice, as of a write, for every record of every store that
      feeds WATCH — the watch's ID, <service>/subscription/<name>, as the
      graph names it —: what a start queues after a stop that was not clean,
      on demand. The watch hears them at the product's next start. A watch
      hears at least once: a record it already heard of is heard of again,
      which its handler, idempotent, tolerates. A deletion is not replayed.
      The product must be stopped: its stores and queues belong to one
      process.
`

// The watch command (ADR 0178 C6): a watch's catch-up, on an operator's
// demand — after a handler fixed, a dead-lettered notice given up on, a
// module mounted on stores that already hold records. The product's binary
// is the tool, as for its revisions; `kit watch …` runs it in dev.
//
//	watch replay WATCH
//
// It runs with the product stopped, as the privacy and revisions commands
// do (withData): the watch's queue — its table on the database that keeps
// the watch's service, else its directory under the data directory — is
// opened in this process, the notices are queued in it, and the watch hears
// them at the next start. It asks for no confirmation: a replay destroys
// nothing, and a watch's contract is already at least once — a notice heard
// twice is a notice its idempotent handler hears again. Each store it lists
// the keys of is one `list` entry in the privacy journal, as Records.Keys
// journals one per listing: a key can be personal data.

// watchCommand runs `watch …`.
func (a *App) watchCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "replay" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		watchUsage(stderr, a.name)
		return 2
	}
	id := args[1]
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx = withActor(ctx, actorCLI)
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		w, err := a.watchNamed(id, stderr)
		if err != nil {
			return err
		}
		stores, queued, err := w.replayNow(ctx, a)
		if err != nil {
			fmt.Fprintln(stdout, say("watch.replay-partial", "watch", w.id, "notices", queued))
			return err
		}
		fmt.Fprintln(stdout, say("watch.replayed", "watch", w.id, "notices", queued, "stores", stores))
		return nil
	})
}

// watchNamed is the watch whose ID is id, or a refusal that names it — and
// says on stderr which watches the product declares.
func (a *App) watchNamed(id string, stderr io.Writer) (*Watch, error) {
	if w, ok := a.findNode(id).(*Watch); ok {
		return w, nil
	}
	known := a.watchIDs()
	if len(known) == 0 {
		fmt.Fprintln(stderr, "the product declares no watch")
	} else {
		fmt.Fprintln(stderr, "the product's watches: "+strings.Join(known, ", "))
	}
	cause := failure(CodeWatchReplay, "WATCH_UNKNOWN", "no such watch: "+clip(id), nil, errs.String("watch", clip(id)))
	return nil, NotFound("no such watch: " + clip(id)).Wrap(cause)
}

// watchIDs are the IDs of the watches the app mounts, sorted.
func (a *App) watchIDs() []string {
	var out []string
	for _, n := range a.mountedNodes() {
		if w, ok := n.(*Watch); ok {
			out = append(out, w.id)
		}
	}
	slices.Sort(out)
	return out
}

// replayNow queues, in this process — the product stopped —, a notice of a
// write for every record of every store that feeds w, in the watch's own
// queue, and returns how many stores it listed and how many notices it
// queued, the failure that ended it among them. Each store's listing is
// journaled before its first key is read.
func (w *Watch) replayNow(ctx context.Context, a *App) (stores, queued int, err error) {
	broker, db, err := a.openConsumerQueue(&w.nodeBase, w.queueUnder(), w.maxDeliver)
	if err != nil {
		return 0, 0, failure(CodeWatchQueue, "WATCH_QUEUE", "a watch's queue cannot be opened", err, errs.String("watch", w.id))
	}
	r := &watchRun{broker: broker, db: db}
	for _, st := range w.feeders(a) {
		if err := a.journal(ctx, &journalLine{op: model.JournalList, store: st.base().id, reason: "watch replay " + w.id}); err != nil {
			return stores, queued, err
		}
		n, err := w.replayStore(ctx, r, st)
		queued += n
		if err != nil {
			return stores, queued, err
		}
		stores++
	}
	return stores, queued, nil
}

// watchUsage says how to run `watch`, for the product named name.
func watchUsage(w io.Writer, name string) {
	fmt.Fprintf(w, watchUsageText, name)
}
