package app

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// An erasure during ADR 0180's window (step 5b): nothing either data-key
// opens is left of the person in what the product keeps, and a copy it
// cannot reach stops opening on ADR 0142's bound, kit-data-key included.

// copyData copies the data directory dir into to, but its secret store: what
// a backup of the product's data holds.
func copyData(t *testing.T, dir, to string) {
	t.Helper()
	must(t, os.RemoveAll(to))
	copyTree(t, dir, to)
	must(t, os.RemoveAll(filepath.Join(to, besideData)))
}

// restoreData puts the data a backup holds back in dir, keeping dir's
// secret store.
func restoreData(t *testing.T, backup, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	must(t, err)
	for _, e := range entries {
		if e.Name() != besideData {
			must(t, os.RemoveAll(filepath.Join(dir, e.Name())))
		}
	}
	copyTree(t, backup, dir)
}

// copyTree copies the tree src into dst with the modes a data directory and
// its secret store need: 0700 directories, 0600 files.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
	must(t, err)
}

// filesHolding are the files under dir, but the secret store, that hold
// text.
func filesHolding(t *testing.T, dir, text string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if rel, _ := filepath.Rel(dir, path); strings.HasPrefix(rel, besideData) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), text) {
			out = append(out, path)
		}
		return err
	})
	must(t, err)
	return out
}

// A person erased once the data keys moved is unreadable at once under both
// roots: the erasure destroys the one wrapped key they had — under
// app-data-key — and folds the store of data keys, so no file the product
// keeps names it. A copy it cannot reach — a backup of the data taken before
// the move, their key in it wrapped under kit-data-key — opens only while
// kit-data-key keeps the version that wrapped it: kit-data-key is retired
// when its rotation would have pruned that version (ADR 0142's bound), after
// which the backup, restored beside the present secrets, opens nothing of
// the person. Without the retirement the backup opens them for the whole
// window.
func TestAnErasedPersonsBackupStopsOpeningWhenTheFormerDataKeyRetires(t *testing.T) {
	needsFileStore(t)
	ctx := t.Context()
	dir, backup := t.TempDir(), filepath.Join(t.TempDir(), "backup")
	clk := clock.NewManualClock(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC))

	// What a former release kept: every data key under kit-data-key.
	a, cards := startCards(t, dir, Clock(clk))
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Secret: "s1", Note: "n1"}))
	must(t, cards.Insert(ctx, card{ID: "c2", Owner: "bob", Secret: "s2", Note: "n2"}))
	kept := a.secretsNow().kept
	for range 2 {
		v, err := secret.Random(32)()
		must(t, err)
		_, err = kept.Put(ctx, formerSecretPrefix+dataKeySecret, v)
		must(t, err)
	}
	freshSealer(a)
	_, err := a.rewrapNow(ctx, true)
	must(t, err)
	must(t, a.Stop(context.Background()))
	copyData(t, dir, backup)

	// This release moves the keys, then ann is erased.
	clk.Advance(time.Hour)
	a, cards = startCards(t, dir, Clock(clk))
	z, err := a.sealing(ctx)
	must(t, err)
	annRef := z.refs.personRef("ann")
	if _, err := a.erase(withActor(ctx, actorCLI), "the person asked", []string{"ann"}); err != nil {
		t.Fatal(err)
	}
	if got := cardsOf(t, cards); got[0].Note != "" || got[0].Secret != "" || got[1].Note != "n2" {
		t.Fatalf("after the erasure: %+v", got)
	}
	must(t, a.Stop(context.Background()))
	if held := filesHolding(t, dir, annRef); len(held) > 0 {
		t.Errorf("ann's data key is still named in %v", held)
	}

	// Within the bound, the backup still opens ann's card — what ADR 0142
	// says of a wrapped copy outside the store before its version goes, and
	// what makes the last check worth something.
	within := filepath.Join(t.TempDir(), "within")
	copyTree(t, dir, within)
	restoreData(t, backup, within)
	a, cards = startCards(t, within, Clock(clk))
	if got := cardsOf(t, cards); got[0].Note != "n1" {
		t.Fatalf("the backup within the bound reads %+v: the test proves nothing", got)
	}
	must(t, a.Stop(context.Background()))

	// Past the bound, the start retires kit-data-key; the backup, restored
	// beside the secrets, opens nothing of ann.
	clk.Advance(time.Duration(defaultKeep)*defaultRotation + time.Hour)
	a, _ = startCards(t, dir, Clock(clk))
	versions, err := a.secretsNow().kept.Versions(ctx, formerSecretPrefix+dataKeySecret)
	must(t, err)
	must(t, a.Stop(context.Background()))
	if len(versions) != 1 {
		t.Errorf("kit-data-key keeps %d versions past the bound, want its one fresh version", len(versions))
	}
	restoreData(t, backup, dir)
	svc := NewService("moved", "Cards whose data keys move, for the step 5b tests.")
	cards = svc.Store("cards", func(c card) string { return c.ID })
	a = NewApp("moved", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard), Clock(clk))
	if err := a.Start(ctx); err != nil {
		// Its data keys open under neither data-key: the start refuses.
		if !errs.HasCode(err, CodeSealOpen) {
			t.Fatalf("the backup past the bound: %v", err)
		}
		return
	}
	defer func() { must(t, a.Stop(context.Background())) }()
	all, _ := cards.List(ctx)
	for _, c := range all {
		if c.Note == "n1" || c.Secret == "s1" {
			t.Fatalf("ann's backed-up card opened past the bound: %+v", c)
		}
	}
}
