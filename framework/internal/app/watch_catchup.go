package app

import (
	"context"
	"errors"
	"io/fs"
	"path"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A watch's catch-up (ADR 0178 C6). A notice is atomic with its write only
// where its queue and the write's transaction share a database (watch.go);
// everywhere else — a write outside any transaction, a store on another
// database, the data directory, memory — a process that dies between the
// write and its notice loses the notice. A watch marks itself running in the
// data directory when it starts and takes the mark off when it stops cleanly;
// a start that finds the mark knows the last run did not stop cleanly, and
// queues one notice per record of every store that feeds the watch, read
// through the keys alone. A clean stop costs nothing; a crash costs one notice
// per record, which an idempotent handler already tolerates. Every store that
// feeds the watch is caught up, its own database's too: a write outside a
// transaction is not atomic with its notice on any of them. A deletion whose
// notice was lost is not caught up: the keys say what is, not what was. An
// app without a data directory keeps no mark, and does not catch up.

// runningMark is the mark's name in the data directory.
func (w *Watch) runningMark() string {
	return path.Join(w.svc.name, "watches", w.name+".running")
}

// catchUp queues a notice for every record of feeders when the last run left
// its mark, then marks the watch running. A catch-up that fails leaves the
// mark, so the next start catches up again, and says so.
func (w *Watch) catchUp(ctx context.Context, a *App, r *watchRun, feeders []feeder) {
	if a.data == nil {
		return
	}
	mark := w.runningMark()
	if _, err := fs.Stat(a.data, mark); err == nil {
		queued, err := w.replay(ctx, r, feeders)
		if err != nil {
			a.problem(w.id, say("watch.catch-up", "watch", w.id, "detail", errs.PublicOf(err)))
			return
		}
		logger.Info(ctx, a.log, "a watch caught up after a stop that was not clean",
			logger.String("watch", w.id), logger.Int("notices", queued))
	}
	if err := w.markRunning(a, mark); err != nil {
		a.problem(w.id, say("watch.running-mark", "watch", w.id, "detail", describeText(err)))
	}
}

// replay queues a notice, as of a write, for every record of feeders, and
// returns how many it queued.
func (w *Watch) replay(ctx context.Context, r *watchRun, feeders []feeder) (int, error) {
	queued := 0
	for _, st := range feeders {
		n, err := w.replayStore(ctx, r, st)
		queued += n
		if err != nil {
			return queued, err
		}
	}
	return queued, nil
}

// replayStore queues a notice, as of a write, for every record of st — the
// catch-up's and the replay command's one loop —, and returns how many it
// queued before it ended.
func (w *Watch) replayStore(ctx context.Context, r *watchRun, st feeder) (int, error) {
	queued := 0
	store := st.base().id
	for key, err := range st.keys(ctx) {
		if err != nil {
			return queued, err
		}
		payload, err := noticeOf(ctx, store, key, false)
		if err != nil {
			return queued, err
		}
		if err := w.post(ctx, r, store, payload); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// markRunning writes the mark that says the watch runs.
func (w *Watch) markRunning(a *App, mark string) error {
	if err := a.data.MkdirAll(path.Dir(mark), 0o700); err != nil {
		return err
	}
	return a.data.WriteAtomic(mark, []byte(w.id+"\n"), 0o600)
}

// unmark takes the mark off once the watch stopped cleanly: the next start
// has nothing to catch up on.
func (w *Watch) unmark(a *App) {
	if a == nil || a.data == nil {
		return
	}
	if err := a.data.Remove(w.runningMark()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.problem(w.id, say("watch.running-mark", "watch", w.id, "detail", describeText(err)))
	}
}
