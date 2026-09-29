// Package kit — records: the product's data as modules and the Studio reach
// it.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// What a module discovers (ADR 0006 §10, ADR 0008). A module never asks the
// product to name its personal or moderated fields: it reads the
// classification — App.Fields — and reaches the records through an untyped
// port — App.Records — which is what kit's own export and erasure run on.

// The marks.
const (
	// Personal marks a field that holds data about a person: personal,
	// special, or a record's subject.
	Personal Mark = "personal"
	// Special marks a field of GDPR art. 9(1) or art. 10.
	Special Mark = "special"
	// Moderated marks content others see and a moderator may act on.
	Moderated Mark = "moderated"
)

// Fields lists the fields of the app's stores that carry the mark, store by
// store in the order the app mounts them, each in its entity's order.
// Personal lists special fields too: they are personal data.
func (a *App) Fields(m Mark) []FieldRefValue {
	var out []FieldRefValue
	for _, st := range a.productStores() {
		out = append(out, markedFields(st, m)...)
	}
	return out
}

// marked reports whether a field's tag carries the mark.
func marked(t *fieldTag, m Mark) bool {
	switch m {
	case Personal:
		return t.personal()
	case Special:
		return t.effective() == model.ClassSpecial
	case Moderated:
		return t.marks.has(markModerated)
	}
	return false
}

// Records returns the untyped port to the records of the store whose node ID
// is store: what kit's export and erasure run on, and what a module reads
// and erases through. A store the app does not mount is a [NotFound] error.
func (a *App) Records(store string) (RecordsService, error) {
	st, ok := a.findNode(store).(privacyStore)
	if !ok || model.ServiceOf(store) == privacyService {
		return RecordsService{}, NotFound("no such store: " + clip(store))
	}
	return NewRecordsService(st.records()), nil
}

// RecordsOf is [App.Records] for code the app runs and that does not hold
// it — a module's watch, its endpoints: the records port of the store whose
// node ID is store, in the app ctx runs in. Outside a running app it is an
// [Unavailable] error.
//
//	func Screen(ctx context.Context, w kit.Written) error {
//		recs, err := kit.RecordsOf(ctx, w.Store)
//		…
//		record, err := recs.Get(ctx, w.Key)
func RecordsOf(ctx context.Context, store string) (RecordsService, error) {
	a := appOf(ctx)
	if a == nil || !a.running() {
		return RecordsService{}, Unavailable("kit.RecordsOf runs inside a running app: call it from a watch, an endpoint, a job or a loop")
	}
	return a.Records(store)
}

// RecordsService is one store's records without their type. Get returns a record
// without its secret members; EraseFields and Delete take a reason and are
// refused on a held record. Each call is a span on the store's node and an
// entry in the privacy journal.
type RecordsService struct {
	port recordsPort
}

// recordsPort is a store's side of Records.
type recordsPort interface {
	recordGet(ctx context.Context, key string) (json.RawMessage, error)
	recordEraseFields(ctx context.Context, key, reason string, paths []string) error
	recordDelete(ctx context.Context, key, reason string) error
}

// Get returns the record under key as JSON, its secret members left out.
func (r RecordsService) Get(ctx context.Context, key string) (json.RawMessage, error) {
	if r.port == nil {
		return nil, NotFound("no such store")
	}
	return r.port.recordGet(ctx, key)
}

// EraseFields clears the fields at paths — pointers as [FieldRefValue] gives
// them — in the record under key: the final removal of a moderated field.
// A path that names no classified field is an [Invalid] error; a held
// record is a [Conflict].
func (r RecordsService) EraseFields(ctx context.Context, key, reason string, paths ...string) error {
	if r.port == nil {
		return NotFound("no such store")
	}
	return r.port.recordEraseFields(ctx, key, reason, paths)
}

// Delete removes the record under key; a held record is a [Conflict].
func (r RecordsService) Delete(ctx context.Context, key, reason string) error {
	if r.port == nil {
		return NotFound("no such store")
	}
	return r.port.recordDelete(ctx, key, reason)
}

// records is the store's side of Records.
//
// IFACE-PLUGIN: a module reaches any store's records through this port,
// whatever the store's type.
func (s *StoreService[T]) records() recordsPort { return s }

// recordGet reads the record key for a module, inside its span.
func (s *StoreService[T]) recordGet(ctx context.Context, key string) (json.RawMessage, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeReads, "", "Get")
	out, err := s.readForModule(ctx, a, key)
	sp.end(err)
	return out, err
}

// readForModule is the record under key without its secret members,
// journaled as read.
func (s *StoreService[T]) readForModule(ctx context.Context, a *App, key string) (json.RawMessage, error) {
	v, err := s.read(ctx, key)
	if err != nil {
		return nil, err
	}
	recs, err := s.exportRecords(ctx, []string{key})
	if err != nil || len(recs) != 1 {
		return nil, err
	}
	return recs[0], a.journal(ctx, &journalLine{op: model.JournalRead, store: s.id, key: key, subject: s.subjectOf(v)})
}

// recordEraseFields erases the fields at paths of the record key for a module,
// for reason.
func (s *StoreService[T]) recordEraseFields(ctx context.Context, key, reason string, paths []string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "", "EraseFields")
	err := s.eraseFields(ctx, a, key, reason, paths)
	sp.end(err)
	return err
}

// eraseFields clears the named fields of one record, unless a hold keeps
// it, journals it, and folds the store — even when the journal failed.
func (s *StoreService[T]) eraseFields(ctx context.Context, a *App, key, reason string, paths []string) error {
	members, err := s.membersAt(paths)
	if err != nil {
		return err
	}
	unlock := a.lockHolds(s.id)
	defer unlock()
	if err := s.holdRefusal(ctx, a, key); err != nil {
		return err
	}
	subject, err := s.clearMembers(ctx, key, members)
	if err != nil {
		return err
	}
	err = a.journal(ctx, &journalLine{op: model.JournalEraseFields, store: s.id, key: key, subject: subject, reason: reason})
	return errors.Join(err, s.fold(ctx))
}

// membersAt are the store's classified members at paths; a path that names
// none is an [Invalid] error.
func (s *StoreService[T]) membersAt(paths []string) ([]*member, error) {
	if len(paths) == 0 {
		return nil, Invalid("EraseFields names at least one field")
	}
	plan := s.plan()
	members := make([]*member, 0, len(paths))
	for _, p := range paths {
		m := plan.member(p)
		if m == nil {
			return nil, Invalid(fmt.Sprintf("%s has no classified field at %q", s.name, clip(p)))
		}
		members = append(members, m)
	}
	return members, nil
}

// clearMembers clears the members of the record under key, in one update,
// and returns the record's subject. Their former values go with them (ADR
// 0007).
func (s *StoreService[T]) clearMembers(ctx context.Context, key string, members []*member) (subject string, err error) {
	plan := s.plan()
	_, err = s.modify(s.erasing(ctx, members), key, func(v *T) error {
		rv := reflect.ValueOf(v).Elem()
		subject, _ = plan.subjectOf(rv)
		for _, m := range members {
			clearPath(rv, m.path)
		}
		if s.keyOf(*v) != key {
			return errRekeyed
		}
		return nil
	})
	switch {
	case errors.Is(err, errRekeyed):
		return "", Invalid(fmt.Sprintf("%s: the key of a record is built from the fields named: they cannot be cleared", s.name))
	case err != nil:
		return "", err
	}
	return subject, nil
}

// recordDelete deletes the record key for a module, for reason.
func (s *StoreService[T]) recordDelete(ctx context.Context, key, reason string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "", "Delete")
	err := s.deleteForModule(ctx, a, key, reason)
	sp.end(err)
	return err
}

// deleteForModule deletes the record under key, unless a hold keeps it,
// journals it, and folds the store: a module's final removal.
func (s *StoreService[T]) deleteForModule(ctx context.Context, a *App, key, reason string) error {
	unlock := a.lockHolds(s.id)
	defer unlock()
	if err := s.holdRefusal(ctx, a, key); err != nil {
		return err
	}
	v, err := s.read(ctx, key)
	if err != nil {
		return err
	}
	removed, err := s.deleteRecord(ctx, a, key, v, reason)
	if removed {
		err = errors.Join(err, s.fold(ctx))
	}
	return err
}

// fieldsOf is a store's classified fields, by class, as the register lists
// them.
func fieldsOf(plan *classPlan) (personal, special, secret []string) {
	for _, m := range plan.members {
		switch m.tag.effective() {
		case model.ClassPersonal:
			personal = append(personal, m.pointer)
		case model.ClassSpecial:
			special = append(special, m.pointer)
		case model.ClassSecret:
			secret = append(secret, m.pointer)
		}
	}
	return slices.Clip(personal), slices.Clip(special), slices.Clip(secret)
}

// NewRecordsService is the records of the store port reaches: [App.Records]
// and [RecordsOf] make one for a store the app mounts.
func NewRecordsService(port recordsPort) RecordsService { return RecordsService{port: port} }
