// Package kit — the privacy journal: what kit did to personal data, and why.
package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/hash"
)

// The privacy journal (ADR 0006 §7). Every export, erasure, deletion by
// retention or on request, hold placed or lifted is an entry: what was done,
// where, by whom — a user's ID, the node that called, "retention", "cli" or
// "studio" — and when, with the subject's reference and the record's. It
// never records an identity, a value or a key. Following Vigie's design,
// each entry is chained to the previous one by SHA-256, so an entry changed
// or removed breaks the chain where it was: `<product> privacy journal
// -verify` checks it. Removing the last entries leaves a chain that holds:
// seeing it needs an anchor outside the store, which the SDK's append-only
// log will give.
//
// An entry is written once its operation stands. When the journal cannot be
// written, the caller gets the error and the operation stays done: an
// erasure is not undone for want of its entry, and what it overwrote still
// leaves the store's files.
//
// A hash-chained append-only log is a mechanism the SDK lacks, so the
// journal is a store of kit's own, kit.privacy/store/journal, on the SDK's
// document store, until the SDK has one (step 4). The chain is computed over
// an entry's JSON before anything seals it: sealing (step 3) must hash
// first, and verify what it opened.

// journalRecord is one entry, in kit's own store.
type journalRecord struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Op      string    `json:"op"`
	DryRun  bool      `json:"dryRun,omitempty"`
	Store   string    `json:"store,omitempty"`
	Record  string    `json:"record,omitempty"`
	Subject string    `json:"subject,omitempty"`
	By      string    `json:"by"`
	// Reason is what the caller said, kept for whoever runs the product:
	// the Studio never shows it.
	Reason string `json:"reason,omitempty" kit:"secret"`
	Hash   string `json:"hash"`
}

// key is the entry's key in the store: its number, zero-padded so that the
// store's key order is the journal's.
func (e *journalRecord) key() string { return fmt.Sprintf("%020d", e.Seq) }

// journalLine is what a caller journals. key and subject are the record's
// key and its subject's identity: the journal keeps their references only.
type journalLine struct {
	op     string
	dryRun bool
	store  string
	key    string
	// subject is an identity; subjectRef, when set, is already a reference.
	subject, subjectRef string
	reason              string
}

// journal appends one entry to the privacy journal. An app that keeps no
// personal data has no journal: there is nothing to say.
func (a *App) journal(ctx context.Context, l *journalLine) error {
	_, j := a.privacyStores()
	if j == nil {
		return nil
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return err
	}
	e := journalRecord{At: a.clock.Now().UTC(), Op: l.op, DryRun: l.dryRun, Store: l.store, By: actorOf(ctx), Reason: l.reason, Subject: l.subjectRef}
	if l.key != "" {
		e.Record = keys.recordRef(l.store, l.key)
	}
	if l.subject != "" {
		e.Subject = keys.subjectRef(l.subject)
	}
	a.privacy.chain.Lock()
	defer a.privacy.chain.Unlock()
	if err := a.loadChain(ctx, j); err != nil {
		return err
	}
	e.Seq = a.privacy.seq + 1
	e.Hash = chainHash(a.privacy.last, e)
	if err := j.write(ctx, e, insertOnly); err != nil {
		return failure(CodePrivacyJournal, "JOURNAL_WRITE", "the privacy journal could not be written", err)
	}
	a.privacy.seq, a.privacy.last = e.Seq, e.Hash
	return nil
}

// journalSubjects journals one operation on a store for each of ids.
func (a *App) journalSubjects(ctx context.Context, op, store string, ids []string) error {
	for _, id := range ids {
		if err := a.journal(ctx, &journalLine{op: op, store: store, subject: id}); err != nil {
			return err
		}
	}
	return nil
}

// loadChain reads the journal's last entry once per run: the next entry
// follows it. The caller holds the chain's lock.
func (a *App) loadChain(ctx context.Context, j *StoreService[journalRecord]) error {
	if a.privacy.loaded {
		return nil
	}
	all, err := j.all(ctx)
	if err != nil {
		return failure(CodePrivacyJournal, "JOURNAL_READ", "the privacy journal could not be read", err)
	}
	a.privacy.seq, a.privacy.last = 0, ""
	if n := len(all); n > 0 {
		a.privacy.seq, a.privacy.last = all[n-1].Seq, all[n-1].Hash
	}
	a.privacy.loaded = true
	return nil
}

// chainHash is an entry's link: SHA-256 of the previous entry's hash and of
// this entry's JSON without its own hash.
func chainHash(prev string, e journalRecord) string {
	e.Hash = ""
	raw, err := json.Marshal(e)
	if err != nil {
		panic("kit: a journal entry does not encode: " + err.Error()) // an entry is strings, numbers and a time
	}
	sum, err := hash.SumHex(hash.SHA256, append([]byte(prev+"\n"), raw...))
	if err != nil {
		panic("kit: SHA-256 is not registered: " + err.Error()) // the SDK registers it
	}
	return sum
}

// verifyJournal checks the chain from its first entry: it breaks where an
// entry was changed, or at the number an entry removed leaves missing. The
// last entries removed leave a chain that holds: seeing them needs an anchor
// outside the store, which the SDK's append-only log will give (ADR 0006,
// step 4).
func (a *App) verifyJournal(ctx context.Context) (model.JournalCheck, error) {
	all, err := a.allJournal(ctx)
	if err != nil {
		return model.JournalCheck{}, err
	}
	return verifyChain(all), nil
}

// verifyChain checks entries, in the store's key order.
func verifyChain(all []journalRecord) model.JournalCheck {
	check := model.JournalCheck{Entries: len(all)}
	prev := ""
	for i, e := range all {
		if want := int64(i + 1); e.Seq != want {
			check.BrokenAt = want
			return check
		}
		if chainHash(prev, e) != e.Hash {
			check.BrokenAt = e.Seq
			return check
		}
		prev = e.Hash
	}
	return check
}

// journalEntries are the journal's latest entries, newest first, as the
// model says them: without their reasons. A journal kit cannot read is an
// error, never an empty journal.
func (a *App) journalEntries(ctx context.Context, limit int) ([]model.JournalEntry, error) {
	all, err := a.allJournal(ctx)
	if err != nil || all == nil {
		return nil, err
	}
	slices.Reverse(all)
	all = all[:min(limit, len(all))]
	out := make([]model.JournalEntry, len(all))
	for i, e := range all {
		out[i] = model.JournalEntry{Seq: e.Seq, At: e.At, Op: e.Op, DryRun: e.DryRun, Store: e.Store, Record: e.Record, Subject: e.Subject, By: e.By, Hash: e.Hash}
	}
	return out, nil
}

// allJournal is the whole journal, in order; none when the app keeps no
// personal data.
func (a *App) allJournal(ctx context.Context) ([]journalRecord, error) {
	_, j := a.privacyStores()
	if j == nil {
		return nil, nil
	}
	if j.engine() == nil {
		return nil, notRunning(&j.nodeBase)
	}
	all, err := j.all(ctx)
	if err != nil {
		return nil, failure(CodePrivacyJournal, "JOURNAL_READ", "the privacy journal could not be read", err)
	}
	return all, nil
}
