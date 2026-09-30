// Package kit — the revisions command: a version of a record written back.
package kit

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/kitsunium/sdk/framework/model"
)

// revisionsUsageText is `revisions`' usage; %[1]s is the product's name.
const revisionsUsageText = `usage:
  %[1]s revisions restore -store ID -key KEY -rev N [-field PATH]…
      write version N of the record back, as a new version by nobody — the
      workflows' states and the secret members kept; -field restores that
      member alone. The product must be stopped: a store belongs to one
      process.
`

// The revisions command (ADR 0007 §6): a version of a record written back,
// as Store.Restore writes it. The Studio shows the versions and the command
// that restores one; restoring is the command line's (ADR 0010, D13). The
// product's binary is the tool, as for its privacy; `kit revisions …` runs
// it in dev.
//
//	revisions restore -store ID -key KEY -rev N [-field PATH]…

// revisionsCommand runs `revisions …`.
func (a *App) revisionsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "restore" {
		revisionsUsage(stderr, a.name)
		return 2
	}
	req, ok := parseRestore(args[1:], stderr)
	if !ok {
		revisionsUsage(stderr, a.name)
		return 2
	}
	store, key, rev, fields := &req.store, &req.key, &req.rev, req.fields
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx = withActor(ctx, actorCLI)
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		st, ok := a.findNode(*store).(revisionsSource)
		if !ok {
			return NotFound("no such store: " + clip(*store))
		}
		if err := st.restoreVia(ctx, a, actorCLI, *key, *rev, fields); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s: version %d restored, as a new version\n", *store, *key, *rev)
		return nil
	})
}

// restoreRequest is what `revisions restore` asks: the store, the record's
// key, the version and the fields to restore alone.
type restoreRequest struct {
	store, key string
	rev        uint64
	fields     []string
}

// parseRestore reads the flags of `revisions restore`; false when they are
// wrong or missing, the flag set having said why.
func parseRestore(args []string, stderr io.Writer) (restoreRequest, bool) {
	var req restoreRequest
	fs := flag.NewFlagSet("revisions restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&req.store, "store", "", "the store's `id`: <service>/store/<name>")
	fs.StringVar(&req.key, "key", "", "the record's `key`")
	fs.Uint64Var(&req.rev, "rev", 0, "the version's `number`, from 1")
	fs.Func("field", "a JSON `pointer` to restore alone (repeatable); none restores the whole record", func(v string) error {
		req.fields = append(req.fields, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return req, false
	}
	return req, req.store != "" && req.key != "" && req.rev != 0 && fs.NArg() == 0
}

// restoreVia restores a version for kit itself — the command line —: no
// node caused it, so no edge is drawn; the restore is one root span on the
// store, which says who asked (via).
func (s *StoreService[T]) restoreVia(ctx context.Context, a *App, via, key string, number uint64, fields []string) error {
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, label: restoreLabel, op: model.OpWrite, name: "Restore"})
	sp.attr("via", via)
	_, err := s.restore(ctx, key, number, fields)
	sp.end(err)
	return err
}

// revisionsUsage says how to run `revisions`, for the product named name.
func revisionsUsage(w io.Writer, name string) {
	fmt.Fprintf(w, revisionsUsageText, name)
}
