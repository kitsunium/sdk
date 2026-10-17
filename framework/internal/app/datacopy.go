package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// `data copy --to NAME` (ADR 0178 C7): the data directory copied into a
// database — every store the database keeps, with its versions, its trash and
// its records' former values; kit's own stores but the data keys when the
// database is SQLite, which never keep them (database_place.go); every
// workflow's journal over those stores; and the waiting and dead messages of
// every file queue the database now keeps, a mailer's outbox among them. It
// runs with the product stopped, refuses a database whose tables hold a row,
// and copies sealed boxes as they are: no key changes hands, and a box is
// opened only to read a mail's fingerprint and a few records back.
//
// The copy is one transaction on the database, and it is declared done only
// when the fingerprints match, never on a count (datacopy_digest.go): each
// store's running SHA-256 over its records — each document, each version,
// its place in the trash — in key order, and each queue's messages as a
// sorted multiset. A store whose fingerprints differ is walked again on both
// sides to name the first key that differs, and the transaction rolls back:
// the database is as it was. Once it has committed, each file queue it
// copied is moved aside under <data>/.data-copy/<instant>/, so the start no
// longer reports it (consumer.file-queue-left); the stores' files stay where
// they are, which the start no longer reads.

// dataCopyDir is where a copy moves the file queues it copied, under the
// data directory: nothing reads it, and an operator may remove it.
const dataCopyDir string = ".data-copy"

// dataUsage is the data command's usage.
const dataUsage string = "usage: %s data copy --to DATABASE   the data directory copied into DATABASE, verified by fingerprints\n"

// dataCopy is one run of the copy: the tables it fills on the database.
type dataCopy struct {
	stores []copyPart
	queues []queuePart
}

// dataCommand runs `data copy --to NAME`.
func (a *App) dataCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	to, ok := dataCopyArgs(args, stderr)
	if !ok {
		fmt.Fprintf(stderr, dataUsage, a.name)
		return 2
	}
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	err := a.copyData(ctx, to, stdout)
	switch {
	case err == nil:
		return 0
	case isDiagnostics(err):
		fmt.Fprintln(stderr, err)
		return exitConfig
	}
	public := errs.PublicOf(err)
	if public == "" {
		public = describeText(err)
	}
	fmt.Fprintln(stderr, public)
	if detail, said := copyDetail(err); said {
		fmt.Fprint(stderr, detail)
	}
	return 1
}

// dataCopyArgs reads `copy --to NAME`: the database's name, and whether the
// arguments are that.
func dataCopyArgs(args []string, stderr io.Writer) (string, bool) {
	if len(args) == 0 || args[0] != "copy" {
		return "", false
	}
	fs := flag.NewFlagSet("data copy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "the database the data directory is copied into")
	if err := fs.Parse(args[1:]); err != nil || *to == "" || fs.NArg() > 0 {
		return "", false
	}
	return *to, true
}

// copyDetail says where a copy that was refused or diverged stands — the
// table, the first key that differs, the counts of a queue —, and whether it
// has anything to say.
func copyDetail(err error) (string, bool) {
	if !errs.HasCode(err, CodeDataCopyDiverged) && !errs.HasCode(err, CodeDataCopy) {
		return "", false
	}
	var b bytes.Buffer
	for _, f := range errs.FieldsOf(err) {
		fmt.Fprintf(&b, "  %s: %s\n", f.Key(), f.StringValue())
	}
	return b.String(), b.Len() > 0
}

// copyData copies the data directory into the database named to.
func (a *App) copyData(ctx context.Context, to string, stdout io.Writer) (err error) {
	target, err := a.copyTarget(ctx, to)
	if err != nil {
		return err
	}
	if err := a.openSecrets(ctx); err != nil {
		return err
	}
	defer a.closeSecrets()
	if err := a.mount(); err != nil {
		return err
	}
	defer a.unmount()
	fsys, err := vfs.NewOS(a.dataDir)
	if err != nil {
		return failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be opened", err, errs.String("dir", a.dataDir))
	}
	a.data = fsys
	runs := a.newDatabaseRuns()
	defer func() { err = errors.Join(err, closeRuns(runs)) }()
	started, err := a.startForCopy(ctx, runs, target)
	defer func() {
		for _, st := range slices.Backward(started) {
			err = errors.Join(err, st.stop(context.WithoutCancel(ctx), a))
		}
	}()
	if err != nil {
		return err
	}
	r := a.runOf(target)
	if err := a.startDatabase(ctx, r); err != nil {
		return err
	}
	if !r.open() {
		return failure(CodeDataCopy, "DATA_COPY_REFUSED", "the database has no URL: there is nothing to copy into", nil, errs.String("database", target.name))
	}
	return a.copyInto(ctx, r, stdout)
}

// copyTarget is the database named to, refused with why a copy into it
// cannot run: not declared, no engine, data kept in memory, or what the
// data directory and the product refuse (dataReady).
func (a *App) copyTarget(ctx context.Context, to string) (*database, error) {
	var target *database
	for _, db := range a.opts.databases {
		if db.name == to {
			target = db
		}
	}
	switch {
	case target == nil:
		return nil, failure(CodeDataCopy, "DATA_COPY_REFUSED", "the product declares no database of that name", nil, errs.String("database", clip(to)))
	case a.opts.memory:
		return nil, failure(CodeDataCopy, "DATA_COPY_REFUSED", "the app keeps its data in memory (app.InMemory): it has no data directory to copy", nil)
	case target.engine == nil:
		return nil, failure(CodeDataCopy, "DATA_COPY_REFUSED", "the database has no engine: the product's main imports none for it", nil, errs.String("database", target.name))
	}
	return target, a.dataReady(ctx)
}

// startForCopy opens every database but the target as the start opens it,
// then the stores: every store the target keeps starts under the data
// directory — the copy's source —, since the target is not open yet. It
// returns the stores it started, for the caller to stop.
func (a *App) startForCopy(ctx context.Context, runs []*databaseRun, target *database) ([]starter, error) {
	for _, r := range runs {
		if r.d == target || r.d.engine == nil {
			continue
		}
		if err := a.startDatabase(ctx, r); err != nil {
			return nil, err
		}
	}
	return a.startStores(ctx)
}

// copyInto copies what the database r keeps, in one transaction, verified by
// fingerprints, then moves the copied file queues aside.
func (a *App) copyInto(ctx context.Context, r *databaseRun, stdout io.Writer) (err error) {
	c, err := a.dataCopyParts(ctx, r)
	defer func() {
		for _, q := range c.queues {
			err = errors.Join(err, q.closeFrom())
		}
	}()
	if err != nil {
		return err
	}
	if err := c.refuseRows(ctx); err != nil {
		return err
	}
	counts := map[string]int{}
	err = r.database().Transact(ctx, func(ctx context.Context) error {
		if err := c.copyAll(ctx, counts); err != nil {
			return err
		}
		return c.verifyAll(ctx)
	})
	if err != nil {
		return err
	}
	moved, err := a.moveQueuesAside(c.queues)
	c.report(stdout, counts, moved)
	return err
}

// report says what the copy wrote, table by table, and where it moved the
// file queues it copied.
func (c *dataCopy) report(w io.Writer, counts map[string]int, moved string) {
	for _, p := range c.stores {
		fmt.Fprintf(w, "%s: %d records copied, fingerprints equal\n", p.name, counts[p.name])
	}
	for _, q := range c.queues {
		fmt.Fprintf(w, "%s: %d messages copied, fingerprints equal\n", q.name, counts[q.name])
	}
	if moved != "" {
		fmt.Fprintf(w, "the file queues copied were moved aside to %s\n", moved)
	}
}

// dataCopyParts are the tables the database r keeps and the copy fills:
// each store's, each workflow journal's, each queue's whose file queue holds
// a message.
func (a *App) dataCopyParts(ctx context.Context, r *databaseRun) (dataCopy, error) {
	var c dataCopy
	stores, err := a.storeParts(ctx, r)
	if err != nil {
		return c, err
	}
	journals, err := a.journalParts(r)
	if err != nil {
		return c, err
	}
	c.stores = append(stores, journals...)
	c.queues, err = a.queueParts(ctx, r)
	return c, err
}

// storeParts are the tables of every store r keeps.
func (a *App) storeParts(ctx context.Context, r *databaseRun) ([]copyPart, error) {
	var out []copyPart
	for _, st := range a.placedStores() {
		s, ok := st.(copyPartser)
		if !ok || !a.keptOn(st, r.d) {
			continue
		}
		parts, err := s.copyParts(ctx, a, r)
		if err != nil {
			return nil, err
		}
		out = append(out, parts...)
	}
	return out, nil
}

// journalParts are the journals of every workflow over a store r keeps.
func (a *App) journalParts(r *databaseRun) ([]copyPart, error) {
	var out []copyPart
	for _, n := range a.mountedNodes() {
		w, ok := n.(journalParter)
		if !ok || w.journalStore() == nil || !a.keptOn(w.journalStore(), r.d) {
			continue
		}
		p, err := w.journalPart(a, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// refuseRows refuses a database whose tables hold a row: a copy writes into
// empty tables only, so that what it verifies is what it wrote.
func (c *dataCopy) refuseRows(ctx context.Context) error {
	for _, p := range c.stores {
		if empty, err := docsEmpty(ctx, p.to); err != nil || !empty {
			return notEmpty(p.name, err)
		}
	}
	for i := range c.queues {
		if empty, err := c.queues[i].empty(ctx); err != nil || !empty {
			return notEmpty(c.queues[i].name, err)
		}
	}
	return nil
}

// notEmpty is the refusal of a table that holds rows, or err when the table
// could not be read.
func notEmpty(table string, err error) error {
	if err != nil {
		return err
	}
	return failure(CodeDataCopy, "DATA_COPY_TARGET_NOT_EMPTY", "a table of the database already holds rows: a copy writes into empty tables only", nil,
		errs.String("table", table))
}

// docsEmpty reports whether a store's table holds no document and no trash.
func docsEmpty(ctx context.Context, docs copyDocs) (bool, error) {
	for _, err := range docs.KeyList(ctx) {
		return false, err
	}
	tombs, err := docs.TombstoneList(ctx)
	return len(tombs) == 0, err
}

// copyAll copies every table, counting what it wrote in counts.
func (c *dataCopy) copyAll(ctx context.Context, counts map[string]int) error {
	for _, p := range c.stores {
		n, err := copyDocuments(ctx, p)
		if err != nil {
			return err
		}
		counts[p.name] = n
	}
	for i := range c.queues {
		n, err := c.queues[i].copy(ctx)
		if err != nil {
			return err
		}
		counts[c.queues[i].name] = n
	}
	return nil
}

// verifyAll compares the fingerprints of both sides of every table and reads
// a few records of each store that seals back through kit, inside the
// copy's transaction: a difference rolls it back.
func (c *dataCopy) verifyAll(ctx context.Context) error {
	for _, p := range c.stores {
		if err := verifyDocuments(ctx, p); err != nil {
			return err
		}
		if p.sample == nil {
			continue
		}
		if err := p.sample(ctx); err != nil {
			return failure(CodeDataCopyDiverged, "DATA_COPY_DIVERGED", "a record copied does not open through kit", err, errs.String("table", p.name))
		}
	}
	for i := range c.queues {
		if err := c.queues[i].verify(ctx); err != nil {
			return err
		}
	}
	return nil
}

// copyDocuments writes every document of p's source into its table: with
// its versions as they are, and the trash at its instants, when the store
// keeps versions; as it rests otherwise. It says how many records it wrote.
func copyDocuments(ctx context.Context, p copyPart) (int, error) {
	n := 0
	for key, err := range p.from.KeyList(ctx) {
		if err != nil {
			return n, err
		}
		if err := copyOne(ctx, p, key, time.Time{}); err != nil {
			return n, err
		}
		n++
	}
	tombs, err := p.from.TombstoneList(ctx)
	if err != nil {
		return n, err
	}
	for _, t := range tombs {
		if err := copyOne(ctx, p, t.Key, t.At); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// copyOne writes the document key of p's source into its table.
func copyOne(ctx context.Context, p copyPart, key string, trashedAt time.Time) error {
	if !p.from.KeepsVersions() {
		raw, err := p.from.Current(ctx, key)
		if err != nil {
			return err
		}
		return p.to.InsertRaw(ctx, raw)
	}
	versions, err := p.from.VersionList(ctx, key)
	if err != nil {
		return err
	}
	return p.to.PutDocument(ctx, key, versions, trashedAt)
}

// moveQueuesAside moves each copied file queue under
// <data>/.data-copy/<instant>/, keeping its path below the data directory,
// and says where; nothing when the copy moved no queue.
func (a *App) moveQueuesAside(queues []queuePart) (string, error) {
	if len(queues) == 0 {
		return "", nil
	}
	aside := filepath.Join(a.dataDir, dataCopyDir, a.clock.Now().UTC().Format("20060102T150405Z"))
	var failed []error
	for _, q := range queues {
		failed = append(failed, q.closeFrom(), moveAside(a.dataDir, q.dir, aside))
	}
	if err := errors.Join(failed...); err != nil {
		return aside, failure(CodeDataCopy, "DATA_COPY_MOVE",
			"the copy is done, but a file queue it copied could not be moved aside: the start keeps reporting it", err)
	}
	return aside, nil
}

// moveAside moves dir, under the data directory data, to the same path
// under aside.
func moveAside(data, dir, aside string) error {
	rel, err := filepath.Rel(data, dir)
	if err != nil {
		return err
	}
	dst := filepath.Join(aside, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(dir, dst)
}
