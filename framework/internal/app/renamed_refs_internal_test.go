package app

import (
	"context"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The references at rest, sealed again (ADR 0180 step 5c-2): a pass stopped
// at any of its writes leaves everything readable, and the next pass — in
// the same run, or the start after a restart — ends where an uninterrupted
// one does; a hold filed again under its present reference beside the
// former one is listed once, and the next pass removes the former.

// relabelApp runs a store of cards that keeps versions, on the data in dir.
func relabelApp(t *testing.T, dir string) (*App, *StoreService[card]) {
	t.Helper()
	pinDataKeyWithoutFileStore(t, "relabel")
	svc := NewService("relabel", "Cards a former release sealed, for the re-seal tests.")
	cards := svc.Store("cards", func(c card) string { return c.ID }, Revisions(3))
	app := NewApp("relabel", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	return app, cards
}

// asFormerRelease makes the app's references, until restore, the ones a
// former release made: every data key named and every hold filed under the
// kit/v1 labels.
func asFormerRelease(t *testing.T, app *App) (restore func()) {
	t.Helper()
	keys, err := app.derivedKeys(t.Context())
	must(t, err)
	z, err := app.sealing(t.Context())
	must(t, err)
	former := &derivedKeys{present: keys.former, former: keys.former}
	app.privacy.mu.Lock()
	app.privacy.keys, z.refs, z.dual = former, keys.former, former
	app.privacy.mu.Unlock()
	return func() {
		app.privacy.mu.Lock()
		app.privacy.keys, z.refs, z.dual = keys, keys.present, keys
		app.privacy.mu.Unlock()
	}
}

// seedFormer writes, as a former release: ann's card written twice — a former
// note under her key —, bob's held then written again — under its own key —,
// and cy's whose subject became dee — cy's former note under cy's key.
func seedFormer(t *testing.T, app *App, cards *StoreService[card]) {
	t.Helper()
	ctx := t.Context()
	restore := asFormerRelease(t, app)
	defer restore()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "ann's first"}))
	must(t, cards.Put(ctx, card{ID: "c1", Owner: "ann", Note: "ann's second"}))
	must(t, cards.Insert(ctx, card{ID: "c2", Owner: "bob", Note: "bob's first"}))
	must(t, cards.Hold(ctx, "c2", "a court"))
	must(t, cards.Put(ctx, card{ID: "c2", Owner: "bob", Note: "bob's second"}))
	must(t, cards.Insert(ctx, card{ID: "c3", Owner: "cy", Note: "cy's"}))
	must(t, cards.Put(ctx, card{ID: "c3", Owner: "dee", Note: "dee's"}))
}

// readCards is what the app reads of the cards and their holds, as text.
func readCards(t *testing.T, app *App, cards *StoreService[card]) string {
	t.Helper()
	ctx := t.Context()
	var b strings.Builder
	for _, key := range []string{"c1", "c2", "c3"} {
		c, err := cards.Get(ctx, key)
		must(t, err)
		former, err := cards.Former(ctx, key, "/note")
		must(t, err)
		revs, err := cards.Revisions(ctx, key)
		must(t, err)
		fmt.Fprintf(&b, "%+v former:", c)
		for _, f := range former {
			fmt.Fprintf(&b, " %s", f.Value)
		}
		b.WriteString(" versions:")
		for _, r := range revs {
			fmt.Fprintf(&b, " %d=%+v", r.Number, r.Value)
		}
		b.WriteString("\n")
	}
	all, err := app.allHolds(ctx)
	must(t, err)
	for _, h := range all {
		fmt.Fprintf(&b, "hold %s %s %s\n", h.Store, h.Key, h.Reason)
	}
	return b.String()
}

// formerLeft are the former references what the app keeps still names: a
// box of a card, a former value, a version, a hold's filing — but cy's former
// note, under a person the card no longer names.
func formerLeft(t *testing.T, app *App, cards *StoreService[card]) []string {
	t.Helper()
	ctx := t.Context()
	keys, err := app.derivedKeys(ctx)
	must(t, err)
	var froms []string
	for _, who := range []string{"ann", "bob", "dee"} {
		froms = append(froms, keys.former.personRef(who))
	}
	for _, key := range []string{"c1", "c2", "c3"} {
		froms = append(froms, keys.former.ownRef(cards.id, key))
	}
	se := cards.sealing()
	var left []string
	for _, key := range []string{"c1", "c2", "c3"} {
		record, former, versions, err := cards.underAny(ctx, se, key, true, froms)
		must(t, err)
		if record || former || versions {
			left = append(left, fmt.Sprintf("%s record=%v former=%v versions=%v", key, record, former, versions))
		}
	}
	holds, _ := app.privacyStores()
	all, err := holds.all(ctx)
	must(t, err)
	for _, h := range all {
		if h.ID != keys.present.recordRef(h.Store, h.Key) {
			left = append(left, "hold "+h.Key+" filed under its former reference")
		}
	}
	return left
}

// faults counts the writes of the engines it wraps, on one counter, and
// refuses the failAt-th while on.
type faults struct {
	mu        sync.Mutex
	on        bool
	n, failAt int
	refused   bool
}

// write counts one write, and says whether it is refused.
func (f *faults) write() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.on {
		return nil
	}
	f.n++
	if f.n == f.failAt {
		f.refused = true
		return errInjected
	}
	return nil
}

// faultyEngine is an engine whose writes faults counts and may refuse; the
// rest goes to the engine it wraps.
type faultyEngine struct {
	plug.StoreEngine
	f *faults
}

func (e *faultyEngine) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	if err := e.f.write(); err != nil {
		return err
	}
	return e.StoreEngine.Write(ctx, d, mode)
}

func (e *faultyEngine) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	if err := e.f.write(); err != nil {
		return plug.DocumentValue{}, err
	}
	return e.StoreEngine.Update(ctx, key, fn)
}

func (e *faultyEngine) Delete(ctx context.Context, key string) error {
	if err := e.f.write(); err != nil {
		return err
	}
	return e.StoreEngine.Delete(ctx, key)
}

func (e *faultyEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return e.StoreEngine.(plug.VersionKeeper).StoredVersions(ctx, key)
}

func (e *faultyEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	if err := e.f.write(); err != nil {
		return err
	}
	return e.StoreEngine.(plug.VersionKeeper).RewriteVersions(ctx, key, fn)
}

func (e *faultyEngine) Fold(ctx context.Context) error {
	if f, ok := e.StoreEngine.(plug.Folder); ok {
		return f.Fold(ctx)
	}
	return nil
}

func (e *faultyEngine) KeyedEntries(ctx context.Context, limit int) ([]plug.EntryValue, error) {
	return e.StoreEngine.(plug.EntrySource).KeyedEntries(ctx, limit)
}

func (e *faultyEngine) Keys(ctx context.Context, after string) iter.Seq2[string, error] {
	return e.StoreEngine.(plug.Keyser).Keys(ctx, after)
}

func (e *faultyEngine) Trash(ctx context.Context, key string) error {
	return e.StoreEngine.(plug.Trasher).Trash(ctx, key)
}

func (e *faultyEngine) Untrash(ctx context.Context, key string) error {
	return e.StoreEngine.(plug.Trasher).Untrash(ctx, key)
}

func (e *faultyEngine) Purge(ctx context.Context, key string) error {
	return e.StoreEngine.(plug.Trasher).Purge(ctx, key)
}

func (e *faultyEngine) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	return e.StoreEngine.(plug.Trasher).Tombstones(ctx)
}

// withFaults wraps the engines the pass writes through — the cards', their
// history's and the holds' — under f, and returns what unwraps them.
func withFaults(app *App, cards *StoreService[card], f *faults) (unwrap func()) {
	holds, _ := app.privacyStores()
	se, hs, h := cards.sealing(), holds.sealing(), cards.historied()
	inner, holdsInner, hist := se.inner, hs.inner, h.hist
	se.inner, hs.inner, h.hist = &faultyEngine{inner, f}, &faultyEngine{holdsInner, f}, &faultyEngine{hist, f}
	return func() { se.inner, hs.inner, h.hist = inner, holdsInner, hist }
}

// A pass refused at its n-th write, for every n until one is never reached,
// leaves everything readable as it was; the next pass, run at once or by the
// start after a restart, leaves nothing under a former reference and reads
// the same as an uninterrupted pass does.
func TestAReSealInterruptedAtEachWriteResumes(t *testing.T) {
	needsFileStore(t)
	var want string
	for n := 0; ; n++ {
		for _, restart := range []bool{false, true} {
			dir := t.TempDir()
			app, cards := relabelApp(t, dir)
			seedFormer(t, app, cards)
			before := readCards(t, app, cards)
			f := &faults{on: true, failAt: n}
			unwrap := withFaults(app, cards, f)
			app.relabel(t.Context())
			f.on = false
			unwrap()
			if got := readCards(t, app, cards); got != before {
				t.Fatalf("n=%d: after the interrupted pass the app reads\n%s\nwas\n%s", n, got, before)
			}
			if restart {
				must(t, app.Stop(t.Context()))
				app, cards = relabelApp(t, dir) // its start runs the pass
			} else {
				app.relabel(t.Context())
			}
			got := readCards(t, app, cards)
			if want == "" {
				want = got
			}
			if got != want || got != before {
				t.Errorf("n=%d restart=%v: the pass resumed reads\n%s\nwant\n%s", n, restart, got, want)
			}
			if left := formerLeft(t, app, cards); len(left) > 0 {
				t.Errorf("n=%d restart=%v: still under a former reference: %v", n, restart, left)
			}
			must(t, app.Stop(t.Context()))
			if n > 0 && !f.refused {
				if restart {
					t.Logf("a pass writes %d times", n-1)
					return
				}
			}
		}
	}
}

// A hold filed under its present reference beside the one a former release
// filed — a pass stopped between the two writes — is one hold: listed once,
// found, and the next pass removes the former, its record held all along.
func TestAHoldFiledTwiceIsOneHold(t *testing.T) {
	needsFileStore(t)
	app, cards := relabelApp(t, t.TempDir())
	defer func() { must(t, app.Stop(t.Context())) }()
	seedFormer(t, app, cards)
	ctx := t.Context()
	holds, _ := app.privacyStores()
	keys, err := app.derivedKeys(ctx)
	must(t, err)
	all, err := holds.all(ctx)
	must(t, err)
	if len(all) != 1 || all[0].ID != keys.former.recordRef(cards.id, "c2") {
		t.Fatalf("the former hold: %+v", all)
	}
	twin := all[0]
	twin.ID = keys.present.recordRef(cards.id, "c2")
	must(t, holds.write(ctx, twin, plug.WriteInsert))
	listed, err := app.allHolds(ctx)
	must(t, err)
	if len(listed) != 1 || listed[0].ID != twin.ID {
		t.Errorf("a hold filed twice is listed %+v", listed)
	}
	if _, held, err := app.holdOf(ctx, cards.id, "c2"); err != nil || !held {
		t.Errorf("the record is not held: %v %v", held, err)
	}
	app.relabel(ctx)
	rest, err := holds.all(ctx)
	must(t, err)
	if len(rest) != 1 || rest[0].ID != twin.ID || !slices.Equal([]string{rest[0].Reason}, []string{"a court"}) {
		t.Errorf("after the pass, the holds: %+v", rest)
	}
	if err := cards.Delete(ctx, "c2"); err == nil {
		t.Error("the held record was deleted")
	}
}

// An erasure that runs while the pass moves a person's records never leaves a
// key of theirs behind: whichever goes first, both of the person's data keys
// are gone once both end, and nothing of theirs reads — the pass holds the
// stripes of the keys it moves shared, the erasure takes them alone.
func TestAnErasureDuringTheReSealLeavesNoKey(t *testing.T) {
	needsFileStore(t)
	for round := range 10 {
		app, cards := relabelApp(t, t.TempDir())
		seedFormer(t, app, cards)
		ctx := t.Context()
		done := make(chan struct{})
		go func() {
			defer close(done)
			app.relabel(ctx)
		}()
		if _, err := app.erase(ctx, "the person asked", []string{"ann"}); err != nil {
			t.Fatalf("round %d: the erasure: %v", round, err)
		}
		<-done
		keys, err := app.derivedKeys(ctx)
		must(t, err)
		for _, ref := range keys.personRefs("ann") {
			if _, err := app.privacyKeyStore().engine().Get(ctx, ref); !plug.Refused(err, plug.RefusalNotFound) {
				t.Errorf("round %d: ann's data key %s outlived the erasure: %v", round, ref, err)
			}
		}
		c, err := cards.Get(ctx, "c1")
		must(t, err)
		former, err := cards.Former(ctx, "c1", "/note")
		must(t, err)
		if c.Note != "" || slices.ContainsFunc(former, func(f Former) bool { return strings.Contains(string(f.Value), "ann") }) {
			t.Errorf("round %d: ann reads after her erasure: %+v %+v", round, c, former)
		}
		must(t, app.Stop(ctx))
	}
}
