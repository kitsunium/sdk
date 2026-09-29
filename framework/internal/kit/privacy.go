// Package kit — privacy: kit's own service for holds and the journal.
package kit

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
)

// kit's own privacy service, as the graph names and describes it.
const (
	// privacyService is the service's name.
	privacyService = "kit.privacy"
	// privacyDoc is the service's documentation.
	privacyDoc = "kit's own service: the legal holds and the privacy journal of the personal data the product keeps (ADR 0006)."
)

// Who did it, in the journal, when no user is in the context.
const (
	actorRetention = "retention"
	actorCLI       = "cli"
)

// Personal data, as the classification promises it (ADR 0006 §2, §6).
//
// Every record of a store whose entity has a subject field is filed under
// its subject's reference: HMAC-SHA256 of the identity as the product
// stores it, under a subkey of kit's generated secret index-key, in an index
// kit adds to the store (privacy_keys.go). The reference tells whoever reads
// the store nothing about the identity, and finds a person's records in
// every store with one lookup per store. The index is rebuilt from the
// documents at every start, so a lost index-key costs nothing but new
// references.
//
// kit.Export gives a person their records (privacy_export.go); kit.Erase
// erases them, or deletes them where the store says kit.DeleteOnErasure;
// Store.Erase erases one record now (privacy_erase.go). A held record is
// left in place (hold.go). Each is a span on the store's node and an entry
// in the privacy journal (journal.go).
//
// Sealing at rest — per-subject data keys, the held records moved under keys
// of their own, the subject's key destroyed on erasure — is step 3 of the
// ADR and needs an SDK change: the seams it plugs into are sealedAtRest
// (classify.go), eraseRecord and deleteRecord (where a record's boxes are
// opened and its key destroyed), and the journal's JournalShred entry.

// Privacy is kit's own service: the legal holds and the privacy journal of
// an app that keeps personal data. kit mounts a copy of it — named
// kit.privacy, a name no service of a product can take — in every app whose
// stores keep personal data or declare a retention, before the product's
// services. Its stores stay in the data directory unless the product places
// them (ADR 0004). A product never mounts it itself.
var Privacy = &Service{name: privacyService, doc: privacyDoc, ids: map[string]bool{}}

// actorKey carries who kit acts for: the retention, the command line, the
// Studio.
type actorKey struct{}

// privacyRun is what an app keeps for personal data: kit's own service and
// its stores, the reference keys, and the journal's chain.
type privacyRun struct {
	mu sync.Mutex
	// svc is kit's own service, mounted while the app keeps personal data;
	// holds and journal are its stores.
	svc     *Service
	holds   *StoreService[holdRecord]
	journal *StoreService[journalRecord]
	// keys are the reference keys, derived from index-key the first time a
	// run needs them.
	keys *referenceKeys
	// explain says the classification's warnings: in dev, and in the config
	// and privacy commands.
	explain bool
	// holdLocks are the stores' hold locks, by store ID (hold.go).
	holdLocks sync.Map

	// chain guards the journal's last entry: its number and its hash.
	chain  sync.Mutex
	loaded bool
	seq    int64
	last   string
}

// privacyStore is a store, whatever its entity type, as kit's privacy sees
// it.
type privacyStore interface {
	node
	retentionRunner
	plan() *classPlan
	privacyDeclared() bool
	retains() bool
	retainOnce(ctx context.Context, a *App, mode string) (erased, deleted int, err error)
	privacyInfo(a *App) *model.StorePrivacy
	registerLine(a *App) model.RegisterStore
	subjectKeys(ctx context.Context, a *App, ids []string) ([]string, error)
	exportOf(ctx context.Context, keys []string, preview bool) (storeExport, error)
	eraseOnRequest(ctx context.Context, a *App, key, reason string) (erasureOutcome, error)
	holdKey(ctx context.Context, a *App, key, reason string) error
	heldCount(a *App) int
	fold(ctx context.Context) error
	records() recordsPort
}

// withActor returns ctx acting for who.
func withActor(ctx context.Context, who string) context.Context {
	return context.WithValue(ctx, actorKey{}, who)
}

// actorOf is who a journal entry says did it: kit's own actor, else the
// user, else the node that called, else kit.
func actorOf(ctx context.Context) string {
	if who, ok := ctx.Value(actorKey{}).(string); ok && who != "" {
		return who
	}
	if uid, ok := UserID(ctx); ok {
		return string(uid)
	}
	return cmp.Or(currentNode(ctx), "kit")
}

// beginPrivacy opens a privacy operation's span on a store's node: a read
// for a reads edge, a write otherwise. Who acts is read first: inside the
// span, the store's node would stand for them.
func (a *App) beginPrivacy(ctx context.Context, store string, edge model.EdgeKind, label, name string) (context.Context, *span) {
	op := model.OpWrite
	if edge == model.EdgeReads {
		op = model.OpRead
	}
	ctx = withActor(ctx, actorOf(ctx))
	return a.begin(ctx, &spanStart{node: store, from: currentNode(ctx), edge: edge, label: label, op: op, name: name})
}

// mountPrivacy puts kit's own service first among the app's services when
// the app keeps personal data, and forgets what the last run derived.
func (a *App) mountPrivacy() {
	a.privacy.mu.Lock()
	a.privacy.keys = nil
	a.privacy.explain = a.privacy.explain || a.cfg.env == EnvDev
	mounted := a.privacy.svc != nil
	a.privacy.mu.Unlock()
	a.privacy.chain.Lock()
	a.privacy.loaded = false
	a.privacy.chain.Unlock()
	if mounted || !a.keepsPersonalData() {
		return
	}
	svc := &Service{name: privacyService, doc: privacyDoc, decl: a.decl, ids: map[string]bool{}}
	holds := svc.Store("holds", func(h holdRecord) string { return h.key() })
	journal := svc.Store("journal", func(e journalRecord) string { return e.key() })
	// kit's own declarations point at the app that brought them.
	holds.decl, journal.decl = a.decl, a.decl
	a.privacy.mu.Lock()
	a.privacy.svc, a.privacy.holds, a.privacy.journal = svc, holds, journal
	a.privacy.mu.Unlock()
	// A copy of an app (With) carries the services its original mounted,
	// kit's own copy among them: this app mounts its own instead. kit.Privacy
	// itself, given to NewApp, stays — and is refused as mounted twice.
	others := slices.DeleteFunc(slices.Clone(a.services), func(s *Service) bool { return s != Privacy && kitOwn(s) })
	a.services = slices.Concat([]*Service{svc}, others)
}

// kitOwn reports whether svc is kit's own service: the copy of kit.Privacy
// kit mounts in the app.
func kitOwn(svc *Service) bool { return svc != nil && svc.name == privacyService }

// asKept is the service a database's Keeps names for svc: kit.Privacy for
// kit's own copy of it, which the product cannot name, svc itself
// otherwise. ADR 0004's placement reads kit's own service by that name.
func asKept(svc *Service) *Service {
	if kitOwn(svc) {
		return Privacy
	}
	return svc
}

// privacyStores returns kit's own stores, nil when the app keeps no
// personal data.
func (a *App) privacyStores() (*StoreService[holdRecord], *StoreService[journalRecord]) {
	a.privacy.mu.Lock()
	defer a.privacy.mu.Unlock()
	return a.privacy.holds, a.privacy.journal
}

// keepsPersonalData reports whether a store of the product keeps personal
// data or declares what it does with it.
func (a *App) keepsPersonalData() bool {
	for _, st := range a.productStores() {
		if st.plan().personal() || st.privacyDeclared() {
			return true
		}
	}
	return false
}

// productStores are the stores of the product's services — kit's own left
// out — in the order the app mounts them.
func (a *App) productStores() []privacyStore {
	var out []privacyStore
	for _, svc := range a.services {
		if svc == nil || kitOwn(svc) {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if st, ok := n.(privacyStore); ok {
				out = append(out, st)
			}
		}
	}
	return out
}

// isNotFound reports whether err is kit's NotFound.
func isNotFound(err error) bool {
	ke, ok := errors.AsType[*Error](err)
	return ok && ke.Code == WireNotFound
}

// isConflict reports whether err is kit's Conflict.
func isConflict(err error) bool {
	ke, ok := errors.AsType[*Error](err)
	return ok && ke.Code == WireConflict
}

// describeText is what describe says of err: wire-safe text for a log line.
func describeText(err error) string {
	_, body := describe(err)
	return body.Message
}

// explainPrivacy makes the classification's warnings said outside dev:
// the config and privacy commands explain what the start would not.
func (a *App) explainPrivacy() {
	a.privacy.mu.Lock()
	a.privacy.explain = true
	a.privacy.mu.Unlock()
}

// privacySettings are the settings of an app that keeps personal data:
// what its retention does, and whether its index key is set, and where —
// never its value.
func (a *App) privacySettings(ctx context.Context) []model.Setting {
	if !a.keepsPersonalData() {
		return nil
	}
	_, from, err := a.kitSecret(ctx, indexSecret)
	if err != nil {
		from = ""
	}
	return []model.Setting{a.retentionSetting(), {Name: "KIT_INDEX_KEY", Secret: true, From: settingFrom(from)}}
}

// settingFoundIn corrects where the start's configuration says a setting
// came from, once kit made it: kit's index key is made after the
// configuration was read.
func (a *App) settingFoundIn(name, from string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.rt.config {
		if a.rt.config[i].Name == name {
			a.rt.config[i].From = from
		}
	}
}
