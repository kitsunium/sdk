package kit

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
)

// kit's own store code — the privacy journal and the holds it mounts, the
// sealing of what rests, the databases and their migrations, the retention,
// the trash, and the commands that read or change what the stores keep — names
// no SDK type, and stays in the core (ADR 0179 §3); a program reaches it only
// through storeCode, which framework/kit/store sets as it is imported
// (EnableStores), as framework/kit/server sets kit's HTTP start. A program
// that does not import it links none of that code: the linker drops what
// nothing reaches. Each call site falls back to what an app without a store
// does — nothing to mount, start, warn of or describe — or, for a command
// that works on the stores, to the refusal that names the import.

// storeHooks are the entries of kit's own store code that the rest of the
// core calls, each a function of the app. Their signatures name no type of
// the store family: a type a reachable value names is linked with its
// methods.
type storeHooks struct {
	// mountPrivacy mounts kit's own service when the app keeps personal
	// data or seals what it keeps.
	mountPrivacy func(a *App)
	// addDatabases, addRetention and addTrash register the components of
	// the databases, the stores' retention and their trash.
	addDatabases, addRetention, addTrash func(a *App) error
	// journal appends an entry to the privacy journal.
	journal func(a *App, ctx context.Context, l *journalLine) error
	// problems are what the stores, their sealing, kit's index key and
	// the databases declare wrong; privacyProblems what the privacy
	// declarations do.
	problems, privacyProblems func(a *App) []model.Diagnostic
	// commands are Main's commands on the stores: migrate, privacy,
	// revisions, data — and watch, whose replay reads a watched store.
	commands map[string]storeCmd
}

// storeCmd is one of Main's commands on the stores.
type storeCmd func(a *App, ctx context.Context, args []string, stdout, stderr io.Writer) int

// storeCode is kit's own store code, nil until framework/kit/store is
// imported.
var storeCode atomic.Pointer[storeHooks]

// EnableStores makes kit's own store code reachable: what kit mounts, starts
// and answers for the stores and databases a product declares.
// framework/kit/store calls it as it is imported; a product never does.
func EnableStores() {
	storeCode.Store(&storeHooks{
		mountPrivacy:    (*App).mountPrivacyService,
		addDatabases:    (*App).addDatabaseComponents,
		addRetention:    (*App).addRetentionComponent,
		addTrash:        (*App).addTrashComponent,
		journal:         (*App).appendToJournal,
		problems:        (*App).storeCodeProblems,
		privacyProblems: (*App).privacyProblems,
		commands: map[string]storeCmd{
			"migrate":   (*App).migrateCommand,
			"privacy":   (*App).privacyCommand,
			"revisions": (*App).revisionsCommand,
			"data":      (*App).dataCommand,
			"watch":     (*App).watchCommand,
		},
	})
}

// stores is kit's own store code, nil in a program that does not import
// framework/kit/store.
func stores() *storeHooks { return storeCode.Load() }

// mountPrivacy puts kit's own service first among the app's services when
// the app keeps personal data or seals what it keeps (privacy.go); nothing
// without the store subsystem, whose stores it mounts.
func (a *App) mountPrivacy() {
	if h := stores(); h != nil {
		h.mountPrivacy(a)
	}
}

// addDatabases registers the databases' components (database_run.go); none
// without the store subsystem, whose start refuses a database.
func (a *App) addDatabases() error {
	if h := stores(); h != nil {
		return h.addDatabases(a)
	}
	return nil
}

// addRetention registers the stores' retention (retention_run.go); none
// without the store subsystem.
func (a *App) addRetention() error {
	if h := stores(); h != nil {
		return h.addRetention(a)
	}
	return nil
}

// addTrash registers the stores' trash (trash_run.go); none without the
// store subsystem.
func (a *App) addTrash() error {
	if h := stores(); h != nil {
		return h.addTrash(a)
	}
	return nil
}

// journal appends one entry to the privacy journal (journal.go); an app
// without the store subsystem has no journal: nothing to say.
func (a *App) journal(ctx context.Context, l *journalLine) error {
	if h := stores(); h != nil {
		return h.journal(a, ctx, l)
	}
	return nil
}

// storeCommand runs Main's command name on the stores, as the store
// subsystem answers it. Without the subsystem the program can declare no
// store and no database, and the command has nothing to work on: it says
// which import it needs and exits 78, as a start refused for a declaration
// does (ADR 0179 §6).
func (a *App) storeCommand(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	if h := stores(); h != nil {
		if cmd := h.commands[name]; cmd != nil {
			return cmd(a, ctx, args, stdout, stderr)
		}
	}
	fmt.Fprintln(stderr, say("subsystem.missing", "node", name, "subsystem", "store", "package", storePackage))
	return exitConfig
}

// storeProblems are what the stores, their sealing, kit's index key and the
// databases declare wrong; none without the store subsystem, whose start
// refuses every store and database (subsystem.go).
func (a *App) storeProblems() []model.Diagnostic {
	if h := stores(); h != nil {
		return h.problems(a)
	}
	return nil
}

// storePrivacyProblems are what the app declares wrong about personal data
// (privacy_info.go): with the store subsystem, privacyProblems, which judges
// the classification of every type as well as the stores' retention; without
// it, the classification alone — a type an endpoint or a topic shows is
// judged whether the program keeps a store or not —, once in either mode.
func (a *App) storePrivacyProblems() []model.Diagnostic {
	if h := stores(); h != nil {
		return h.privacyProblems(a)
	}
	a.privacy.mu.Lock()
	explain := a.privacy.explain
	a.privacy.mu.Unlock()
	return a.classificationProblems(explain)
}

// storeCodeProblems are what the stores' sealing, kit's index key and the
// databases declare wrong, in that order.
func (a *App) storeCodeProblems() []model.Diagnostic {
	return slices.Concat(a.sealingProblems(), a.indexKeyProblems(), a.databaseProblems())
}
