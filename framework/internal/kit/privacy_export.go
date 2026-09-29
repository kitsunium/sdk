// Package kit — export: a person's data as kit gives it back.
package kit

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// PersonalData is a person's data as kit.Export gives it.
type PersonalData = model.PersonalData

// Export returns every record whose subject is one of ids, in every store of
// the app: each without its secret members, with the former values its
// fields keep (ADR 0007) — never a secret field's —, and per store what
// GDPR art. 15(1) asks beside the data — the purpose, the retention and the
// recipients inside the product. A held record is exported like any other.
// The result is JSON, structured and machine-readable (art. 20); keeping the
// month of art. 12(3) is the product's job, and so is deciding who may call
// it.
//
// A person known under several identities — a user's ID in one store, an
// e-mail address in another — is found by giving all of them. ctx must run
// inside the app: an endpoint's, a job's, a loop's.
func Export(ctx context.Context, ids ...string) (PersonalData, error) {
	a := appOf(ctx)
	if a == nil || !a.running() {
		return PersonalData{}, Unavailable("kit.Export runs inside a running app: call it from an endpoint, a job or a loop")
	}
	return a.export(ctx, ids, false)
}

// export gathers a person's records, for them (their values) or for the
// Studio's preview (redacted).
func (a *App) export(ctx context.Context, ids []string, preview bool) (PersonalData, error) {
	out := PersonalData{Stores: []model.StoreData{}}
	ids = identities(ids)
	if len(ids) == 0 {
		return out, Invalid("an export names at least one identity")
	}
	var recipients map[string][]string
	for _, st := range a.productStores() {
		if st.plan().subject == nil {
			continue
		}
		part, err := a.exportStore(ctx, st, ids, preview)
		if err != nil {
			return PersonalData{}, err
		}
		if len(part.records) == 0 {
			continue
		}
		if recipients == nil {
			recipients = a.recipients()
		}
		out.Stores = append(out.Stores, a.storeData(st, part, recipients))
	}
	return out, nil
}

// exportStore reads a person's records in one store, and their former
// values, in a span of its own: their values, journaled, or the Studio's
// redacted preview, which gives nobody their data and is not.
func (a *App) exportStore(ctx context.Context, st privacyStore, ids []string, preview bool) (storeExport, error) {
	id := st.base().id
	ctx, sp := a.beginPrivacy(ctx, id, model.EdgeReads, "export", "Export")
	keys, err := st.subjectKeys(ctx, a, ids)
	var part storeExport
	if err == nil && len(keys) > 0 {
		part, err = st.exportOf(ctx, keys, preview)
	}
	if err == nil && len(part.records) > 0 && !preview {
		err = a.journalSubjects(ctx, model.JournalExport, id, ids)
	}
	sp.end(err)
	return part, err
}

// storeExport is one store's records about a person, and their former
// values (ADR 0007).
type storeExport struct {
	records []json.RawMessage
	former  []model.RecordFormer
}

// storeData is a store's part of an export: its records and their former
// values, and what GDPR art. 15(1) asks beside them.
func (a *App) storeData(st privacyStore, part storeExport, recipients map[string][]string) model.StoreData {
	id, line := st.base().id, st.registerLine(a)
	return model.StoreData{
		Store: id, Purpose: line.Purpose, Retention: retentionWords(line), Recipients: recipients[id],
		Records: part.records, Former: part.former,
	}
}

// retentionWords says a store's retention in one sentence.
func retentionWords(l model.RegisterStore) string {
	var parts []string
	if l.Erase != "" {
		parts = append(parts, "personal data erased "+l.Erase)
	}
	if l.Delete != "" {
		parts = append(parts, "records deleted "+l.Delete)
	}
	if l.HeldUntil != "" {
		parts = append(parts, "held "+l.HeldUntil)
	}
	if len(parts) == 0 {
		return "kept until the product deletes them"
	}
	return strings.Join(parts, "; ")
}

// identities are ids without the empty ones and the repeats, in order.
func identities(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// exportOf is the records under keys and their former values, as a person
// receives them — their secret members and fields left out, as deep as a
// record goes —, or as the Studio's preview shows them, every personal,
// special and secret value redacted.
func (s *StoreService[T]) exportOf(ctx context.Context, keys []string, preview bool) (storeExport, error) {
	var out storeExport
	for _, key := range keys {
		raw, found, err := s.exportRecord(ctx, key, preview)
		if err != nil {
			return storeExport{}, err
		}
		if !found {
			continue
		}
		former, err := s.exportFormer(ctx, key, preview)
		if err != nil {
			return storeExport{}, err
		}
		if len(former) > 0 {
			out.former = append(out.former, model.RecordFormer{Record: len(out.records), Fields: former})
		}
		out.records = append(out.records, raw)
	}
	return out, nil
}

// exportRecords are the records under keys as a person receives them: their
// secret members left out, as deep as a record goes.
func (s *StoreService[T]) exportRecords(ctx context.Context, keys []string) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(keys))
	for _, key := range keys {
		raw, found, err := s.exportRecord(ctx, key, false)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, raw)
		}
	}
	return out, nil
}

// exportRecord is the record under key as its person receives it, or as the
// Studio's preview shows it; found is false when there is none.
func (s *StoreService[T]) exportRecord(ctx context.Context, key string, preview bool) (json.RawMessage, bool, error) {
	v, err := s.read(ctx, key)
	switch {
	case isNotFound(err):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case preview:
		raw, _ := redactValue(v, 64<<10)
		return raw, true, nil
	}
	raw, err := json.Marshal(v)
	if err == nil {
		raw, err = s.plan().withoutSecrets(raw)
	}
	if err != nil {
		return nil, false, failure(CodeStoreEncode, "STORE_ENCODE", "the entity cannot be exported", err, errs.String("store", s.id))
	}
	return raw, true, nil
}
