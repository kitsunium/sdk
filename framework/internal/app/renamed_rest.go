package app

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The names at rest, moved (ADR 0180 step 5, Q5). What a former release kept
// under the tool's name is moved to the framework's, once, the first time a
// release of this one meets it, and never copied: a directory by rename(2),
// a table by the database's own renaming, so that every byte it holds —
// sealed or not, a version, the trash, the privacy journal's chain — is the
// one the former release wrote. In the data directory: dev's default .kit/data
// becomes .app/data, and the privacy service's kit.privacy becomes
// app.privacy. On a database: kit_migrations, kit_tables, kit_queue__… and
// kit_privacy__… become app_migrations, app_tables, app_queue__… and
// app_privacy__… (renameTables, and the store subsystem's RenameTables).
//
// A move never replaces. When the former name and the present one both
// exist, the start is refused with CodeRenameTarget, both named, and nothing
// is moved: the former data stays readable by the release that wrote it,
// and which one holds the product's data is the operator's to say. A move
// that did not complete — a rename the system refused, a statement the
// database did not run — is CodeRenameFailed: what was not moved stays
// under its former name, and the next start resumes.
//
// What a sealed value is bound to does not move here: what the privacy
// service's stores sealed stays bound to kit.privacy's ids (sealedAs), and
// the labels of sealing and of the privacy keys keep their words, until
// steps 5b and 5c re-seal them (ADR 0180 Q6). That is what keeps a move
// undoable: renamed back, the data is the former release's again.

// The move of the framework's tables, as its version table records it.
const (
	// renameVersion is the move's version in the former version table:
	// above every version the framework gives a table (a generation in the
	// high 32 bits), so that the SDK's migrator never finds it out of order.
	renameVersion uint64 = math.MaxInt64
	// renameName labels it there.
	renameName = "the framework's tables take the framework's name (ADR 0180 step 5)"
	// renameTakenPublic and renameFailedPublic are the refusals' public texts.
	renameTakenPublic  = "data a former release kept is also under its present name: nothing was moved"
	renameFailedPublic = "data a former release kept could not be moved to its present name: it stays where it was"
)

// renameTaken is the refusal of a move whose present name is taken: what
// was found under each name, both said.
func renameTaken(from, to string) error {
	return errs.New(CodeRenameTarget, "RENAME_TARGET_TAKEN", renameTakenPublic,
		"app: "+from+" holds what a former release kept, and "+to+" is taken too: the framework replaces neither — keep one, move the other aside",
		errs.String("from", from), errs.String("to", to))
}

// renameFailed is the refusal of a move that did not complete.
func renameFailed(from, to string, cause error) error {
	private := "app: " + from + " could not be moved to " + to
	if cause != nil {
		private += ": " + cause.Error()
	}
	return errs.New(CodeRenameFailed, "RENAME_FAILED", renameFailedPublic, private,
		errs.String("from", from), errs.String("to", to))
}

// moveAtRest moves what a former release kept in the data directory to its
// present names: dev's default directory, then the privacy service's
// directory in it. It runs when the app resolves, before anything reads the
// directory. Both moves are judged before either runs, so that a refusal
// moves nothing; and when the second fails once the first ran, the first is
// undone, so that what the former release kept is where it left it.
func (a *App) moveAtRest() error {
	if a.dataDir == "" {
		return nil
	}
	devFrom, err := a.devFormer()
	if err != nil {
		return err
	}
	data := cmp.Or(devFrom, a.dataDir)
	privacy, err := movable(filepath.Join(data, formerPrivacyService), filepath.Join(data, privacyService))
	if err != nil {
		return err
	}
	if err := a.moveBoth(devFrom, privacy); err != nil {
		return err
	}
	a.saidMoves(devFrom, privacy)
	return nil
}

// devFormer is dev's former default directory when it is there to be moved
// to dev's present one, "" otherwise: a directory the operator named is
// the operator's, and nothing is moved into it.
func (a *App) devFormer() (string, error) {
	if a.cfg.formerDataDir == "" {
		return "", nil
	}
	former, err := filepath.Abs(a.cfg.formerDataDir)
	if err != nil {
		return "", renameFailed(a.cfg.formerDataDir, a.dataDir, err)
	}
	found, err := movable(former, a.dataDir)
	if err != nil || !found {
		return "", err
	}
	return former, nil
}

// moveBoth moves dev's former directory devFrom, when there is one, then
// the privacy service's directory, when privacy says it is there; a second
// move that fails renames the first back.
func (a *App) moveBoth(devFrom string, privacy bool) error {
	if devFrom != "" {
		if err := moveDir(devFrom, a.dataDir); err != nil {
			return err
		}
	}
	if !privacy {
		return nil
	}
	err := moveDir(filepath.Join(a.dataDir, formerPrivacyService), filepath.Join(a.dataDir, privacyService))
	if err == nil || devFrom == "" {
		return err
	}
	if undo := os.Rename(a.dataDir, devFrom); undo != nil {
		return errors.Join(err, renameFailed(a.dataDir, devFrom, undo))
	}
	return err
}

// saidMoves says on the app's log what moveBoth moved, and drops the tool's
// directory dev's former one leaves, when nothing is left in it — what the
// tool keeps there stays.
func (a *App) saidMoves(devFrom string, privacy bool) {
	if devFrom != "" {
		a.dropEmpty(filepath.Dir(devFrom))
		a.saidMoved(formerDevDataDir, devDataDir)
	}
	if privacy {
		a.saidMoved(formerPrivacyService, privacyService)
	}
}

// dropEmpty removes the directory dir when it holds nothing; a removal that
// fails is said, and leaves it.
func (a *App) dropEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	if err := os.Remove(dir); err != nil && a.log != nil {
		logger.Warn(context.Background(), a.log, "an empty directory a former release left could not be removed",
			logger.String("dir", dir), logger.String("error", err.Error()))
	}
}

// movable reports whether from is there to be moved to to: false when it
// is not; a to that exists too is CodeRenameTarget.
func movable(from, to string) (bool, error) {
	_, err := os.Lstat(from)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, renameFailed(from, to, err)
	}
	_, err = os.Lstat(to)
	if err == nil {
		return false, renameTaken(from, to)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, renameFailed(from, to, err)
	}
	return true, nil
}

// moveDir moves the directory from to to by rename(2), creating to's
// parent. rename(2) replaces an empty directory: to is judged absent first
// (movable), and the window between the two is a second process starting
// on the same data directory, which a store does not allow.
func moveDir(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return renameFailed(from, to, err)
	}
	if err := os.Rename(from, to); err != nil {
		return renameFailed(from, to, err)
	}
	return nil
}

// saidMoved says on the app's log what a move moved.
func (a *App) saidMoved(from, to string) {
	if a.log != nil {
		logger.Info(context.Background(), a.log, "what a former release kept moved to its present name (ADR 0180 step 5)",
			logger.String("from", from), logger.String("to", to))
	}
}

// sealedAs is what a former release bound a value sealed in the node id to:
// the node's id, the framework's own service's under its former name. A box
// it sealed there is read under it (openMember), and its own data keys are
// named after it (derivedKeys.ownRefs); nothing is sealed under it since ADR
// 0180 step 5c.
func sealedAs(id string) string {
	if rest, ok := strings.CutPrefix(id, privacyService+"/"); ok {
		return formerPrivacyService + "/" + rest
	}
	return id
}

// ownService reports whether service, as a node id or a record names it —
// a journal's entry written by a former release among them —, is the
// framework's own: app.privacy, or kit.privacy as a former release wrote it.
func ownService(service string) bool {
	return service == privacyService || service == formerPrivacyService
}

// The tables ---------------------------------------------------------------

// renamePlan is the move of the framework's tables on d: its registry, every
// table this build keeps there that a former release named otherwise — a
// consumer's queue, the privacy service's stores —, and the names their
// migrations had in the history.
func (a *App) renamePlan(d *database) (plug.RenameSpec, error) {
	plan := plug.RenameSpec{FormerVersions: formerSetTable, Versions: tableKit, Name: renameName, Version: renameVersion}
	registry := []kitTable{{name: kitTablesTable, former: formerTablesTable}}
	for _, t := range append(registry, a.kitTables(d)...) {
		if t.former == "" {
			continue
		}
		if t.versionsOf == "" {
			// A store's versions are derived from its table, and move with it.
			plan.Tables = append(plan.Tables, plug.RenameValue{From: t.former, To: t.name})
		}
		now, err := tableMigration(d.dialect(), t, 1)
		if err != nil {
			return plug.RenameSpec{}, err
		}
		before, err := tableMigration(d.dialect(), t.formerTable(), 1)
		if err != nil {
			return plug.RenameSpec{}, err
		}
		plan.Labels = append(plan.Labels, plug.RenameValue{From: before.Name, To: now.Name})
	}
	return plan, nil
}

// formerTable is t as a former release named it.
func (t kitTable) formerTable() kitTable {
	f := kitTable{name: t.former, node: t.node, queue: t.queue}
	if t.versionsOf != "" {
		f.versionsOf = strings.TrimSuffix(t.former, versionsSuffix)
	}
	return f
}

// formerTablesOn are the framework's tables on d still under a former name:
// what `migrate up` moves, and what a database that migrates manually
// refuses to start with.
func (a *App) formerTablesOn(ctx context.Context, d *database, db plug.Database) ([]string, error) {
	stores := plug.Stores.Load()
	if stores == nil || stores.FormerTables == nil {
		return nil, storesMissing()
	}
	plan, err := a.renamePlan(d)
	if err != nil {
		return nil, err
	}
	return stores.FormerTables(ctx, db, plan)
}

// renameTables moves the framework's tables on d to their present names,
// and says what it moved; nothing when none was left under a former name.
func (a *App) renameTables(ctx context.Context, d *database, db plug.Database) ([]string, error) {
	stores := plug.Stores.Load()
	if stores == nil || stores.RenameTables == nil {
		return nil, storesMissing()
	}
	plan, err := a.renamePlan(d)
	if err != nil {
		return nil, err
	}
	moved, err := stores.RenameTables(ctx, db, plan)
	switch {
	case plug.Refused(err, plug.RefusalRenameTarget):
		var tables string
		if r, ok := errors.AsType[*plug.RefusalError](err); ok {
			tables = fieldOf(r.Err, "tables")
		}
		return nil, errs.New(CodeRenameTarget, "RENAME_TARGET_TAKEN", renameTakenPublic,
			"app: database "+d.name+" holds the framework's tables under their former names and their present names are taken too ("+
				tables+"): the framework replaces neither — keep one, move the other aside",
			errs.String("database", d.name), errs.String("tables", tables))
	case err != nil:
		return nil, renameFailed("the "+formerName+"_* tables of database "+d.name, "their "+frameworkName+"_* names", err)
	}
	if len(moved) > 0 && a.log != nil {
		logger.Info(ctx, a.log, "the framework's tables moved to their present names (ADR 0180 step 5)",
			logger.String("database", d.name), logger.String("tables", strings.Join(moved, ", ")))
	}
	return moved, nil
}

// rekey files the registry's record of each table this build names
// otherwise than a former release did under its present name, once the
// tables moved: what the move left to the registry's rows, which the core
// alone reads. A record already under its present name is kept, and the
// former one dropped.
func (g *tableRegistry) rekey(ctx context.Context, tables []kitTable) error {
	for _, t := range tables {
		if t.former == "" {
			continue
		}
		rec, ok := g.get(t.former)
		if !ok {
			continue
		}
		if _, now := g.get(t.name); !now {
			rec.Table = t.name
			if err := g.put(ctx, rec); err != nil {
				return err
			}
		}
		if err := g.drop(ctx, t.former); err != nil {
			return err
		}
	}
	return nil
}
