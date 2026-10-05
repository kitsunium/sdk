package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// restoreLabel labels Restore's edge, drawn by the runtime and by the
// analyzer alike: a restore is a write the diagram tells apart.
const restoreLabel = "restore"

// Record revisions (ADR 0007 §3). A store declared with kit.Revisions(n)
// keeps the n previous versions of each record, as WordPress keeps a post's
// revisions: numbered from 1, never renumbered, each with when it was made,
// by whom — the caller's kit.UserID — and, when a command made it, which
// command. They live with the record, in the SDK's document store, in the
// record's own durable write (kitsunium/sdk ADR 0143): the same overlay entry
// in the data directory, the same transaction on a database.
//
//   - A write that changes the record makes a version; one that stores the
//     same JSON makes none, and neither does a workflow's transition that
//     changes only the state, which the workflow's journal records.
//   - The oldest beyond n are pruned in the write that makes a newer one,
//     unless a legal hold keeps the record (ADR 0006): its versions pile up
//     until its first write after the release.
//   - Revisions and Revision read them, a secret member zeroed; Diff says
//     what changed between two, a secret member without its values; Restore
//     writes one back — whole, or some of its fields — as a new version,
//     keeping the workflow's state and every secret member.
//   - A version is sealed at rest as its record is, under the same data key:
//     a person's erasure makes it unreadable when their key goes, and kit
//     rewrites it cleared as the record is, so that neither Restore nor the
//     Studio brings back an erased value (revisions_privacy.go).

// revisionsLimit bounds kit.Revisions(n): the history=N bound (ADR 0007).
const revisionsLimit = historyLimit

// What a version's metadata holds: the SDK keeps it with the version and
// never reads it (docstore.Stamp).
const (
	metaBy      = "by"
	metaCommand = "command"
)

// revisionsOption is Revisions' StoreConfigurer.
type revisionsOption struct{ n int }

// storeConfigure sets the store's revisions.
func (o revisionsOption) storeConfigure(so *storeOptions) { so.revisions, so.revisionsSet = o.n, true }

// Revisions keeps the n previous versions of each record of the store, n
// from 1 to 100, as WordPress keeps a post's revisions: every write that
// changes a record makes a version, numbered from 1 and never renumbered,
// stamped with when it was made, the caller's user and the command that
// made it; the oldest beyond n are pruned by the write that makes a newer
// one, unless a legal hold keeps the record.
//
//	var Pages = Service.Store("pages", Page.Key, kit.Revisions(20))
//
//	revs, err := Pages.Revisions(ctx, id)                            // newest first
//	edits, err := Pages.Diff(ctx, id, revs[3].Number, revs[0].Number)
//	page, err := Pages.Restore(ctx, id, revs[3].Number)              // a new version
//	page, err = Pages.Restore(ctx, id, revs[3].Number, "/title")     // the title only
//
// A store with Revisions(n) holds up to n+1 times its records: in memory
// with the store in the data directory, in the table <table>___vs on a
// database, which kit's migrations create.
func Revisions(n int) StoreConfigurer { return revisionsOption{n: n} }

// declareRevisions takes what the store's options say of its versions, and
// says what they get wrong.
func (s *StoreService[T]) declareRevisions(svc *Service, o storeOptions) {
	if !o.revisionsSet {
		return
	}
	if o.revisions < 1 || o.revisions > revisionsLimit {
		svc.problem(s.decl, s.id, "store.revisions", "store", s.name, "n", o.revisions, "max", revisionsLimit)
		return
	}
	s.revisions = o.revisions
}

// Edit is one change between two versions of a record, in RFC 6902's words:
// Op is add, remove or replace, Path the RFC 6901 JSON pointer of the member
// it changes, From the value it had and To the value it has, as JSON. A
// secret member says it changed, never what: its From and To are empty.
type Edit = model.Edit

// Revisions returns the versions the store keeps of the record under key,
// newest first: the record as it is now, then the former ones — n at most,
// more while a legal hold keeps the record. A secret member is zeroed in
// each: a former password hash never reaches the product. A missing record
// is a [NotFound], a store that keeps no revisions an [Invalid].
func (s *StoreService[T]) Revisions(ctx context.Context, key string) ([]RevisionEvent[T], error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Revisions"})
	out, err := s.revisionList(ctx, key)
	sp.end(err)
	return out, err
}

// Revision returns the version numbered number of the record under key, its
// secret members zeroed: a [NotFound] when the record keeps none of that
// number — never made, or pruned.
func (s *StoreService[T]) Revision(ctx context.Context, key string, number uint64) (RevisionEvent[T], error) {
	a := s.app()
	if a == nil {
		return RevisionEvent[T]{}, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Revision"})
	out, err := s.revisionOne(ctx, key, number)
	sp.end(err)
	return out, err
}

// Diff returns what changed from the version numbered from to the version
// numbered to of the record under key, in RFC 6902's words — add, remove,
// replace, each at a JSON pointer — with both values, in the order they
// apply: none when the two are the same. A secret member says it changed,
// never what. Showing a text's changes line by line is the reader's: the
// Studio does it.
func (s *StoreService[T]) Diff(ctx context.Context, key string, from, to uint64) ([]Edit, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Diff"})
	out, err := s.diff(ctx, key, from, to)
	sp.end(err)
	return out, err
}

// Restore writes the version numbered number of the record under key back,
// as a new version: history is never rewritten. It restores the whole
// version, or only the fields named by JSON pointer (RFC 6901) — "/title" —,
// as WordPress restores some fields of a revision. It keeps the record's
// workflow state, which its workflow owns, and every secret member:
// restoring an account never brings back an old password past its policy,
// and naming one of them is an [Invalid] error. A unique index the restored
// record breaks refuses it with a [Conflict], and nothing changes. It
// returns the record as restored.
func (s *StoreService[T]) Restore(ctx context.Context, key string, number uint64, fields ...string) (T, error) {
	a := s.app()
	if a == nil {
		var zero T
		return zero, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeWrites, label: restoreLabel, op: model.OpWrite, name: "Restore"})
	v, err := s.restore(ctx, key, number, fields)
	sp.end(err)
	return v, err
}

// revisionList reads every version of the record under key, newest first,
// decoded, its secret members zeroed.
func (s *StoreService[T]) revisionList(ctx context.Context, key string) ([]RevisionEvent[T], error) {
	all, err := s.openedVersions(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]RevisionEvent[T], 0, len(all))
	for _, v := range all {
		r, err := s.revisionOf(key, v)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// revisionOne reads the version numbered number of the record under key.
func (s *StoreService[T]) revisionOne(ctx context.Context, key string, number uint64) (RevisionEvent[T], error) {
	v, err := s.openedVersion(ctx, key, number)
	if err != nil {
		return RevisionEvent[T]{}, err
	}
	return s.revisionOf(key, v)
}

// revisionOf is a version as the product reads it: decoded, its secret
// members zeroed.
func (s *StoreService[T]) revisionOf(key string, v docstore.Version) (RevisionEvent[T], error) {
	var value T
	if err := json.Unmarshal(v.JSON, &value); err != nil {
		return RevisionEvent[T]{}, s.versionDecodeFailure(key, v.Number, err)
	}
	s.plan().zeroSecrets(reflect.ValueOf(&value).Elem())
	return RevisionEvent[T]{Number: v.Number, At: v.At, By: v.Meta[metaBy], Command: v.Meta[metaCommand], Value: value}, nil
}

// versionDecodeFailure reports a version that no longer fits its type: the
// type changed since the version was made.
func (s *StoreService[T]) versionDecodeFailure(key string, number uint64, err error) error {
	return failure(CodeRevisionDecode, "REVISION_DECODE", "a version of a record does not match its type", err,
		errs.String("store", s.id), errs.String("key", key), errs.String("number", strconv.FormatUint(number, 10)))
}

// zeroSecrets sets every secret member of v to its zero value, as deep as v
// goes. v must be addressable.
func (p *classPlan) zeroSecrets(v reflect.Value) {
	visitFields(v, p.rules, func(f *fieldRule, fv reflect.Value) bool {
		if f.tag.effective() == model.ClassSecret {
			if fv.CanSet() {
				fv.SetZero()
			}
			return false
		}
		return true
	})
}

// openedVersions reads every version the store keeps of the record under
// key, newest first, as JSON — opened, for a store that seals: a member
// whose data key is destroyed is left out.
func (s *StoreService[T]) openedVersions(ctx context.Context, key string) ([]docstore.Version, error) {
	all, err := s.restingVersions(ctx, key)
	if err != nil {
		return nil, err
	}
	se := s.sealing()
	if se == nil {
		return all, nil
	}
	for i := range all {
		opened, err := se.openDoc(ctx, key, all[i].JSON)
		if err != nil {
			return nil, err
		}
		all[i].JSON = opened
	}
	return all, nil
}

// openedVersion reads the version numbered number of the record under key.
func (s *StoreService[T]) openedVersion(ctx context.Context, key string, number uint64) (docstore.Version, error) {
	all, err := s.openedVersions(ctx, key)
	if err != nil {
		return docstore.Version{}, err
	}
	for _, v := range all {
		if v.Number == number {
			return v, nil
		}
	}
	return docstore.Version{}, s.noVersion(key, number)
}

// restingVersions reads every version of the record under key as the store
// keeps it — a sealing store's members as boxes.
func (s *StoreService[T]) restingVersions(ctx context.Context, key string) ([]docstore.Version, error) {
	vk, err := s.keeper()
	if err != nil {
		return nil, err
	}
	all, err := vk.storedVersions(ctx, key)
	if err != nil {
		return nil, s.versionsSaid(err, key)
	}
	return all, nil
}

// keeper is the engine that keeps the store's versions, as they rest: an
// [Invalid] for a store that keeps none.
func (s *StoreService[T]) keeper() (versionKeeper, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	vk, ok := eng.(versionKeeper)
	if s.revisions == 0 || !ok {
		return nil, Invalid(fmt.Sprintf("%s keeps no revisions: declare it with kit.Revisions(n)", s.name))
	}
	return vk, nil
}

// noVersion is the NotFound of a version the record does not keep.
func (s *StoreService[T]) noVersion(key string, number uint64) error {
	return NotFound(fmt.Sprintf("%s: the record %q keeps no version %d: never made, or pruned", s.name, clip(key), number))
}

// versionsSaid is a refusal of the engine's versions in kit's words.
func (s *StoreService[T]) versionsSaid(err error, key string) error {
	switch {
	case errors.Is(err, docstore.VersionsNotKept):
		return Invalid(fmt.Sprintf("%s keeps no revisions: declare it with kit.Revisions(n)", s.name))
	case errors.Is(err, docstore.VersionsRewriteRefused):
		return failure(CodeRevisionWrite, "REVISIONS_REWRITE", "a record's versions could not be rewritten", err, errs.String("store", s.id))
	}
	return s.said(err, key, "")
}

// versionKeeper is an engine that keeps its records' versions: the SDK's
// document store opened with Versions, in memory, in files or over SQL —
// and the engines that wrap one, which hand its versions on as they rest. A
// sibling of the port (store_engine.go).
type versionKeeper interface {
	// storedVersions are the versions of the record under key, newest
	// first, the current one first, as the engine keeps them.
	storedVersions(ctx context.Context, key string) ([]docstore.Version, error)
	// rewriteVersions rewrites the former versions of the record under key
	// as fn returns them, under the engine's writers' lock or in one
	// transaction (docstore's RewriteVersions): fn may drop a version or
	// change what one holds, never add or renumber one.
	rewriteVersions(ctx context.Context, key string, fn func([]docstore.Version) ([]docstore.Version, error)) error
}

// What a write says about the version it makes ----------------------------------

// commandKey carries the node ID of the command a context runs in: the
// command a version names.
type commandKey struct{}

// withCommand is ctx running in the command id: what its writes make names
// it.
func withCommand(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, commandKey{}, id)
}

// commandOf is the command ctx runs in, "" outside one.
func commandOf(ctx context.Context) string {
	id, _ := ctx.Value(commandKey{}).(string)
	return id
}

// inPlaceKey marks a context whose writes change a record without making a
// version: a workflow's transition that changes only the state, a record
// sealed again with the same meaning.
type inPlaceKey struct{}

// withInPlace is ctx whose writes make no version.
func withInPlace(ctx context.Context) context.Context {
	return context.WithValue(ctx, inPlaceKey{}, true)
}

// inPlace reports whether ctx's writes make no version.
func inPlace(ctx context.Context) bool {
	in, _ := ctx.Value(inPlaceKey{}).(bool)
	return in
}

// stampOf is what a write under ctx says about the version it makes: who —
// the caller's user —, which command, and whether it makes none.
func stampOf(ctx context.Context) docstore.Stamp {
	st := docstore.Stamp{InPlace: inPlace(ctx)}
	meta := map[string]string{}
	if uid, ok := UserID(ctx); ok && uid != "" {
		meta[metaBy] = string(uid)
	}
	if cmd := commandOf(ctx); cmd != "" {
		meta[metaCommand] = cmd
	}
	if len(meta) > 0 {
		st.Meta = meta
	}
	return st
}
