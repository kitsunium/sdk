package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/crypto/hash"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
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
// journal is a store of kit's own, app.privacy/store/journal, on the SDK's
// document store, until the SDK has one (step 4). The chain is computed over
// an entry's JSON before anything seals it: sealing (step 3) must hash
// first, and verify what it opened. An entry's reason is sealed under the
// journal's key for the month of its instant, in UTC (journalRef, ADR 0178
// §C3): a key per month, never per entry.
//
// A journal is closed, never rewritten (ADR 0180 step 5c). Every entry this
// release writes says the labels its references are made under (Refs). When
// the journal's last entry does not — a former release wrote it —, the next
// entry is preceded by a JournalContinue entry that names that last entry's
// hash (Follows) and starts a chain of its own: the former journal stays as
// it was, verified as it was, and the chain holds from one to the other.

// journalRefLabels is what an entry this release writes says of its references:
// the labels they are made under (renamed_labels.go).
const journalRefLabels = frameworkName + "/v1"

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
	// Follows is a JournalContinue entry's: the hash of the last entry of
	// the journal it closes.
	Follows string `json:"follows,omitempty"`
	// Refs are the labels Record and Subject are made under: journalRefLabels
	// for every entry this release writes, nothing for a former release's.
	Refs string `json:"refs,omitempty"`
	// Reason is what the caller said, kept for whoever runs the product:
	// the Studio never shows it.
	Reason string `json:"reason,omitempty" app:"secret"`
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

// appendToJournal appends one entry to the privacy journal — inside a transaction,
// once it commits: an entry says what stands, and a rollback drops it. An
// app that keeps no personal data has no journal: there is nothing to say.
func (a *App) appendToJournal(ctx context.Context, l *journalLine) error {
	_, j := a.privacyStores()
	if j == nil {
		return nil
	}
	keys, err := a.journalKeys(ctx)
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
	release := func(rctx context.Context) error { return a.appendJournal(context.WithoutCancel(rctx), j, e) }
	if hold(ctx, heldEffect{node: j.id, release: release}) {
		return nil
	}
	return a.appendJournal(ctx, j, e)
}

// appendJournal chains e after the journal's last entry and writes it.
func (a *App) appendJournal(ctx context.Context, j *StoreService[journalRecord], e journalRecord) error {
	a.privacy.chain.Lock()
	defer a.privacy.chain.Unlock()
	if err := a.loadChain(ctx, j); err != nil {
		return err
	}
	if a.privacy.formerJournal() {
		if err := a.continueJournal(ctx, j, e.At); err != nil {
			return err
		}
	}
	e.Seq, e.Refs = a.privacy.seq+1, journalRefLabels
	e.Hash = chainHash(a.privacy.last, e)
	if err := j.write(ctx, e, plug.WriteInsert); err != nil {
		return failure(CodePrivacyJournal, "JOURNAL_WRITE", "the privacy journal could not be written", err)
	}
	a.privacy.seq, a.privacy.last, a.privacy.lastRefs = e.Seq, e.Hash, e.Refs
	return nil
}

// continueJournal closes the journal a former release kept, as it is, and
// opens the next: one JournalContinue entry, naming the former's last hash,
// its chain starting again (ADR 0180 step 5c). It is the durable mark of the
// closing — written whole or not at all, and once written the journal's last
// entry is this release's, so it is never written twice. It is dated at,
// the instant of the entry it precedes. The caller holds the chain's lock.
func (a *App) continueJournal(ctx context.Context, j *StoreService[journalRecord], at time.Time) error {
	c := journalRecord{Seq: a.privacy.seq + 1, At: at, Op: model.JournalContinue, By: frameworkName, Follows: a.privacy.last, Refs: journalRefLabels}
	c.Hash = chainHash("", c)
	if err := j.write(ctx, c, plug.WriteInsert); err != nil {
		return failure(CodePrivacyJournal, "JOURNAL_WRITE", "the privacy journal could not be written", err)
	}
	if a.log != nil {
		logger.Info(ctx, a.log, "the privacy journal a former release kept is closed as it was, and continued (ADR 0180 step 5c)",
			logger.Int64("closed", a.privacy.seq), logger.Int64("continued", c.Seq))
	}
	a.privacy.seq, a.privacy.last, a.privacy.lastRefs = c.Seq, c.Hash, c.Refs
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
	a.privacy.seq, a.privacy.last, a.privacy.lastRefs = 0, "", ""
	if n := len(all); n > 0 {
		last := all[n-1]
		a.privacy.seq, a.privacy.last, a.privacy.lastRefs = last.Seq, last.Hash, last.Refs
	}
	a.privacy.loaded = true
	return nil
}

// chainHash is an entry's link: SHA-256 of the previous entry's hash — of
// nothing for a JournalContinue entry — and of this entry's JSON without its
// own hash.
func chainHash(prev string, e journalRecord) string {
	e.Hash = ""
	raw, err := json.Marshal(e)
	if err != nil {
		panic("app: a journal entry does not encode: " + err.Error()) // an entry is strings, numbers and a time
	}
	sum, err := hash.SumHex(hash.SHA256, append([]byte(prev+"\n"), raw...))
	if err != nil {
		panic("app: SHA-256 is not registered: " + err.Error()) // the SDK registers it
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

// verifyChain checks entries, in the store's key order: each journal's chain
// from its first entry, and the link from one journal to the next — a
// JournalContinue entry names the hash of the entry before it, which closed
// the journal before (ADR 0180 step 5c). A changed hash in that link, or an
// entry of the closed journal removed or changed, breaks the chain at the
// JournalContinue entry or before it.
func verifyChain(all []journalRecord) model.JournalCheck {
	check := model.JournalCheck{Entries: len(all)}
	prev := ""
	for i, e := range all {
		if want := int64(i + 1); e.Seq != want {
			check.BrokenAt = want
			return check
		}
		link, linked := linkOf(e, prev)
		if !linked || chainHash(link, e) != e.Hash {
			check.BrokenAt = e.Seq
			return check
		}
		if e.Op == model.JournalContinue {
			check.Closed = append(check.Closed, e.Seq-1)
		}
		prev = e.Hash
	}
	return check
}

// linkOf is what e's hash chains from, prev being the hash of the entry
// before it: prev itself, or nothing for a JournalContinue entry, whose
// Follows must be prev — a journal closed there, and none closes before the
// first entry. linked is false where the link breaks: a JournalContinue
// entry naming another hash, or a Follows on any other entry.
func linkOf(e journalRecord, prev string) (link string, linked bool) {
	if e.Op != model.JournalContinue {
		return prev, e.Follows == ""
	}
	return "", prev != "" && e.Follows == prev
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
		out[i] = model.JournalEntry{Seq: e.Seq, At: e.At, Op: e.Op, DryRun: e.DryRun, Store: e.Store, Record: e.Record, Subject: e.Subject, By: e.By, Follows: e.Follows, Hash: e.Hash}
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
