package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The journal a former release kept is closed as it is and continued (ADR
// 0180 step 5c): a JournalContinue entry names its last hash and starts a
// chain of its own; the former journal verifies as it was, its reasons
// opened where a former release sealed them, and the link between the two
// is checked.

// chained is entries chained as appendJournal chains them: each after the
// one before it, a JournalContinue entry from nothing, and numbered from 1.
func chained(entries ...journalRecord) []journalRecord {
	out := slices.Clone(entries)
	prev := ""
	for i := range out {
		out[i].Seq = int64(i + 1)
		link := prev
		if out[i].Op == model.JournalContinue {
			out[i].Follows, link = prev, ""
		}
		out[i].Hash = chainHash(link, out[i])
		prev = out[i].Hash
	}
	return out
}

// rechain recomputes every hash from entry from on, as one who rewrites the
// journal would, leaving each Follows as it is.
func rechain(all []journalRecord, from int) {
	for i := from; i < len(all); i++ {
		link := ""
		if i > 0 && all[i].Op != model.JournalContinue {
			link = all[i-1].Hash
		}
		all[i].Hash = chainHash(link, all[i])
	}
}

// aClosedJournal is three entries of a former journal, the entry that
// continues it, and two of the next.
func aClosedJournal() []journalRecord {
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	return chained(
		journalRecord{At: at, Op: model.JournalErase, Store: "s/store/a", Record: "r1", Subject: "s1", By: "cli", Reason: "asked"},
		journalRecord{At: at, Op: model.JournalShred, Store: "app.privacy/store/keys", Subject: "s1", By: "cli"},
		journalRecord{At: at, Op: model.JournalHold, Store: "s/store/a", Record: "r2", By: "app", Reason: "a court"},
		journalRecord{At: at, Op: model.JournalContinue, By: frameworkName, Refs: journalRefLabels},
		journalRecord{At: at, Op: model.JournalExport, Store: "s/store/a", Subject: "s2", By: "cli", Refs: journalRefLabels},
		journalRecord{At: at, Op: model.JournalErase, Store: "s/store/a", Record: "r3", Subject: "s3", By: "cli", Reason: "asked", Refs: journalRefLabels},
	)
}

// The chain holds across a closed journal and the next, and says where the
// former closed; every way of changing either breaks it, where it was
// changed or at the entry that continues it.
func TestTheChainHoldsAcrossAClosedJournalAndBreaksWhereItIsChanged(t *testing.T) {
	if check := verifyChain(aClosedJournal()); check.BrokenAt != 0 || check.Entries != 6 || !slices.Equal(check.Closed, []int64{3}) {
		t.Fatalf("a closed journal and the next: %+v", check)
	}
	type tc struct {
		name   string
		change func([]journalRecord) []journalRecord
		at     int64
	}
	runCase := func(t *testing.T, c tc) {
		if got := verifyChain(c.change(aClosedJournal())).BrokenAt; got != c.at {
			t.Errorf("the chain breaks at %d, want %d", got, c.at)
		}
	}
	for _, c := range []tc{
		{"an entry of the former journal changed", func(all []journalRecord) []journalRecord {
			all[1].By = "studio"
			return all
		}, 2},
		{"the former journal's last entry changed, its hash rewritten", func(all []journalRecord) []journalRecord {
			all[2].By = "studio"
			rechain(all, 2)
			return all
		}, 4},
		{"the link named another entry's hash, every hash after rewritten", func(all []journalRecord) []journalRecord {
			all[3].Follows = all[1].Hash
			rechain(all, 3)
			return all
		}, 4},
		{"the link changed, its own hash left", func(all []journalRecord) []journalRecord {
			all[3].Follows = strings.Repeat("0", 64)
			return all
		}, 4},
		{"the former journal's last entry removed, the next renumbered and rewritten", func(all []journalRecord) []journalRecord {
			all = slices.Delete(all, 2, 3)
			for i := range all {
				all[i].Seq = int64(i + 1)
			}
			rechain(all, 2)
			return all
		}, 3},
		{"the former journal removed whole", func(all []journalRecord) []journalRecord {
			all = all[3:]
			for i := range all {
				all[i].Seq = int64(i + 1)
			}
			rechain(all, 0)
			return all
		}, 1},
		{"an entry of the next journal changed", func(all []journalRecord) []journalRecord {
			all[4].Subject = "s9"
			return all
		}, 5},
		{"a link on an entry that continues nothing", func(all []journalRecord) []journalRecord {
			all[4].Follows = all[3].Hash
			rechain(all, 4)
			return all
		}, 5},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// formerJournalEntry writes the journal's next entry as a former release wrote it:
// its references made under the kit/v1 labels, no Refs, its reason sealed
// under its month's key bound to kit.privacy/store/journal. The next entry
// the app journals reads the journal again.
func formerJournalEntry(t *testing.T, app *App, op, reason string) journalRecord {
	t.Helper()
	ctx := t.Context()
	_, j := app.privacyStores()
	dual, err := app.derivedKeys(ctx)
	must(t, err)
	keys := dual.former
	app.privacy.chain.Lock()
	defer app.privacy.chain.Unlock()
	must(t, app.loadChain(ctx, j))
	e := journalRecord{Seq: app.privacy.seq + 1, At: app.clock.Now().UTC(), Op: op, Store: "journalkeys/store/cards", Record: keys.recordRef("journalkeys/store/cards", "c1"), By: "cli", Reason: reason}
	e.Hash = chainHash(app.privacy.last, e)
	raw, err := json.Marshal(e)
	must(t, err)
	se := j.sealing()
	sealed, _, err := sealWalk(raw, se.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		if isNull(value) {
			return value, false, nil
		}
		box, err := se.z.seal(ctx, journalRef(e.At), value, sealedAs(j.id), e.key(), at.pointer)
		return box, false, err
	})
	must(t, err)
	must(t, se.inner.Write(ctx, plug.DocumentValue{JSON: sealed, Value: e}, plug.WriteInsert))
	app.privacy.loaded = false
	return e
}

// erased journals one erasure of fields of c1, its reason given.
func erased(t *testing.T, app *App, cards *StoreService[card], reason string) error {
	t.Helper()
	recs, err := app.Records(cards.id)
	must(t, err)
	return recs.EraseFields(t.Context(), "c1", reason, "/note")
}

// continued checks the journal: the former entries as they were, one
// JournalContinue entry naming the last one's hash, then entries of this
// release, every reason opened, the chain holding.
func continued(t *testing.T, app *App, former []journalRecord, reasons ...string) []journalRecord {
	t.Helper()
	all, err := app.allJournal(t.Context())
	must(t, err)
	n := len(former)
	if len(all) != n+1+len(reasons) {
		t.Fatalf("%d entries, want %d former, one that continues them and %d", len(all), n, len(reasons))
	}
	for i, e := range former {
		if got := all[i]; got != e {
			t.Errorf("former entry %d: %+v, was %+v", i+1, got, e)
		}
	}
	c := all[n]
	if c.Op != model.JournalContinue || c.Follows != former[n-1].Hash || c.Refs != journalRefLabels || c.By != frameworkName {
		t.Errorf("the entry that continues the former journal: %+v", c)
	}
	for i, r := range reasons {
		if e := all[n+1+i]; e.Reason != r || e.Refs != journalRefLabels || e.Op == model.JournalContinue {
			t.Errorf("entry %d: %+v, want the reason %q under %s", e.Seq, e, r, journalRefLabels)
		}
	}
	if check := verifyChain(all); check.BrokenAt != 0 || !slices.Equal(check.Closed, []int64{int64(n)}) {
		t.Errorf("the chain: %+v", check)
	}
	return all
}

// A former journal is closed by the next entry the app journals, never
// before and once only: that entry follows a JournalContinue entry, its
// references made under the app/v1 labels, its reason sealed bound to the
// journal's own id; the former entries read as they were. A second start
// continues nothing again.
func TestAFormerJournalIsContinuedOnceByTheNextEntry(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	clk := clock.NewManualClock(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC))
	app, cards := journalApp(t, dir, clk)
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "n"}))
	former := []journalRecord{formerJournalEntry(t, app, model.JournalHold, "a court"), formerJournalEntry(t, app, model.JournalErase, "asked")}
	before, err := app.allJournal(ctx)
	must(t, err)
	if check := verifyChain(before); check.BrokenAt != 0 || check.Closed != nil {
		t.Fatalf("the former journal alone: %+v", check)
	}
	must(t, erased(t, app, cards, "after the close"))
	all := continued(t, app, former, "after the close")
	dual, err := app.derivedKeys(ctx)
	must(t, err)
	if e := all[3]; e.Record != dual.present.recordRef(cards.id, "c1") || e.Record == dual.former.recordRef(cards.id, "c1") {
		t.Errorf("the entry of this release names %q: its record's reference under app/v1 is %q, under kit/v1 %q",
			e.Record, dual.present.recordRef(cards.id, "c1"), dual.former.recordRef(cards.id, "c1"))
	}
	_, j := app.privacyStores()
	raw, err := j.sealing().inner.Get(ctx, all[3].key())
	must(t, err)
	var rest3 struct {
		Reason json.RawMessage `json:"reason"`
	}
	must(t, json.Unmarshal(raw.JSON, &rest3))
	if _, err := j.sealing().z.open(ctx, rest3.Reason, journalID, all[3].key(), "/reason"); err != nil {
		t.Errorf("the reason of this release's entry is not bound to %s: %v", journalID, err)
	}
	must(t, app.Stop(ctx))

	again, cards := journalApp(t, dir, clk)
	must(t, erased(t, again, cards, "after a restart"))
	continued(t, again, former, "after the close", "after a restart")
}

// The former journal's reasons open only where the former release sealed
// them: bound to kit.privacy/store/journal. Without that read the former
// journal no longer opens, so no longer verifies; a box this release seals
// opens under the journal's own id alone.
func TestAFormerJournalsReasonsOpenOnlyWhereTheyWereSealed(t *testing.T) {
	needsFileStore(t)
	clk := clock.NewManualClock(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC))
	app, cards := journalApp(t, t.TempDir(), clk)
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "n"}))
	old := formerJournalEntry(t, app, model.JournalHold, "a court")
	must(t, erased(t, app, cards, "after the close"))
	_, j := app.privacyStores()
	se := j.sealing()
	reasonOf := func(key string) json.RawMessage {
		raw, err := se.inner.Get(ctx, key)
		must(t, err)
		var v struct {
			Reason json.RawMessage `json:"reason"`
		}
		must(t, json.Unmarshal(raw.JSON, &v))
		return v.Reason
	}
	formerBox, presentBox := reasonOf(old.key()), reasonOf((&journalRecord{Seq: 3}).key())
	if _, err := se.z.open(ctx, formerBox, journalID, old.key(), "/reason"); !errs.HasCode(err, CodeSealOpen) {
		t.Errorf("the former reason under the journal's own id alone: %v, want CodeSealOpen", err)
	}
	if plain, err := se.openMember(ctx, formerBox, old.key(), "/reason"); err != nil || string(unpaddedJSON(plain)) != `"a court"` {
		t.Errorf("the former reason where it lies: %q, %v", plain, err)
	}
	if _, err := se.z.open(ctx, presentBox, sealedAs(journalID), "00000000000000000003", "/reason"); !errs.HasCode(err, CodeSealOpen) {
		t.Errorf("this release's reason under kit.privacy's id: %v, want CodeSealOpen", err)
	}
}

// journalID is the privacy journal's node id.
const journalID = privacyService + "/store/journal"

// unpaddedJSON is a JSON value without the spaces sealing padded it with.
func unpaddedJSON(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }

// failingWrites is an engine whose writes fail from the n-th on, n counting
// from 1, until heal.
type failingWrites struct {
	plug.StoreEngine
	writes, failAt int
	healed         bool
}

// errInjected is the failure failingWrites injects.
var errInjected = errors.New("injected: the disk refused the write")

func (f *failingWrites) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	f.writes++
	if !f.healed && f.writes >= f.failAt {
		return errInjected
	}
	return f.StoreEngine.Write(ctx, d, mode)
}

// failJournal makes the journal's writes fail from the n-th on, and returns
// what heals it.
func failJournal(t *testing.T, app *App, n int) func() {
	t.Helper()
	_, j := app.privacyStores()
	j.mu.Lock()
	f := &failingWrites{StoreEngine: j.eng, failAt: n}
	j.eng = f
	j.mu.Unlock()
	return func() {
		j.mu.Lock()
		f.healed = true
		j.mu.Unlock()
	}
}

// A close interrupted at either of its two writes — the JournalContinue
// entry, or the entry it precedes — leaves the journal readable, verified
// and closed at most once: the next entry, in the same run or after a
// restart, completes it, and the journal is the one an uninterrupted close
// writes.
func TestAnInterruptedCloseResumes(t *testing.T) {
	needsFileStore(t)
	type tc struct {
		name    string
		failAt  int
		restart bool
	}
	runCase := func(t *testing.T, c tc) {
		dir := t.TempDir()
		clk := clock.NewManualClock(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC))
		app, cards := journalApp(t, dir, clk)
		ctx := t.Context()
		must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "n"}))
		former := []journalRecord{formerJournalEntry(t, app, model.JournalHold, "a court"), formerJournalEntry(t, app, model.JournalErase, "asked")}
		heal := failJournal(t, app, c.failAt)
		if err := erased(t, app, cards, "refused"); !errs.HasCode(err, CodePrivacyJournal) {
			t.Fatalf("the interrupted entry: %v, want CodePrivacyJournal", err)
		}
		heal()
		all, err := app.allJournal(ctx)
		must(t, err)
		if want := len(former) + c.failAt - 1; len(all) != want {
			t.Fatalf("after the interruption, %d entries, want %d", len(all), want)
		}
		if check := verifyChain(all); check.BrokenAt != 0 {
			t.Fatalf("after the interruption the chain breaks at %d", check.BrokenAt)
		}
		if c.restart {
			must(t, app.Stop(ctx))
			app, cards = journalApp(t, dir, clk)
		}
		must(t, erased(t, app, cards, "resumed"))
		continued(t, app, former, "resumed")
	}
	for _, c := range []tc{
		{"the continuing entry refused", 1, false},
		{"the entry after it refused", 2, false},
		{"the continuing entry refused, then a restart", 1, true},
		{"the entry after it refused, then a restart", 2, true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
