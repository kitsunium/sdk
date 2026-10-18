package app

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The secrets and the sealing label, re-sealed (ADR 0180 step 5b), from
// inside: a move of the data keys stopped anywhere, a box under data-key
// itself sealed under the present label alone, and the former secrets read
// until they are copied.

// startCards starts a store of cards on the data in dir, in dev — its
// secrets in the file store beside the data —, and returns it running.
func startCards(t *testing.T, dir string, opts ...AppConfigurer) (*App, *StoreService[card]) {
	t.Helper()
	svc := NewService("moved", "Cards whose data keys move, for the step 5b tests.")
	cards := svc.Store("cards", func(c card) string { return c.ID })
	a := NewApp("moved", svc).With(append([]AppConfigurer{DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard)}, opts...)...)
	must(t, a.Start(t.Context()))
	return a, cards
}

// cardsOf are every card the store reads, by key order.
func cardsOf(t *testing.T, cards *StoreService[card]) []card {
	t.Helper()
	all, err := cards.List(t.Context())
	must(t, err)
	slices.SortFunc(all, func(x, y card) int {
		switch {
		case x.ID < y.ID:
			return -1
		case x.ID > y.ID:
			return 1
		}
		return 0
	})
	return all
}

// freshSealer drops the run's sealer, so that the next use builds it again
// over what the secret store holds now.
func freshSealer(a *App) {
	a.privacy.mu.Lock()
	a.privacy.sealer = nil
	a.privacy.mu.Unlock()
}

// stopAfter is a store of data keys that ends its pass once it replaced
// replaces keys: a stop between two of the pass's writes.
type stopAfter struct {
	secret.SubjectKeyStore
	replaces int
	cancel   context.CancelFunc
}

// Replace replaces, then ends the pass once enough keys were replaced.
func (s *stopAfter) Replace(ctx context.Context, ref string, current, next []byte) (bool, error) {
	done, err := s.SubjectKeyStore.Replace(ctx, ref, current, next)
	if done {
		if s.replaces--; s.replaces == 0 {
			s.cancel()
		}
	}
	return done, err
}

// formerKeys writes cards in dir — four people, one of them erased, and two
// cards of their own — then puts every data key under kit-data-key, as a
// former release left them, and moves moved of them back under app-data-key
// before the run stops: a move stopped after moved writes. It returns what
// the store read before anything moved, and how many keys there are.
func formerKeys(t *testing.T, dir string, moved int) ([]card, int) {
	t.Helper()
	ctx := t.Context()
	a, cards := startCards(t, dir)
	for _, c := range []card{
		{ID: "c1", Owner: "ann", Secret: "s1", Note: "n1"},
		{ID: "c2", Owner: "bob", Secret: "s2", Note: "n2"},
		{ID: "c3", Owner: "cy", Secret: "s3", Note: "n3"},
		{ID: "c4", Secret: "s4", Note: "n4"},
		{ID: "c5", Secret: "s5", Note: "n5"},
		{ID: "c6", Owner: "dan", Secret: "s6", Note: "n6"},
	} {
		must(t, cards.Insert(ctx, c))
	}
	if _, err := a.erase(withActor(ctx, actorCLI), "the person asked", []string{"cy"}); err != nil {
		t.Fatal(err)
	}
	want := cardsOf(t, cards)
	kept := a.secretsNow().kept
	for range 2 {
		v, err := secret.Random(32)()
		must(t, err)
		_, err = kept.Put(ctx, formerSecretPrefix+dataKeySecret, v)
		must(t, err)
	}
	freshSealer(a)
	back, err := a.rewrapNow(ctx, true)
	must(t, err)
	if back.FromFormer == 0 {
		t.Fatalf("no data key moved back: %+v", back)
	}
	total := back.FromFormer + back.Current
	if moved > 0 {
		z, err := a.sealing(ctx)
		must(t, err)
		stopped, cancel := context.WithCancel(ctx)
		defer cancel()
		sk, err := secret.NewSubjectKeys(secret.SubjectKeysConfig{
			Root: z.root, Former: z.former,
			Store: &stopAfter{SubjectKeyStore: keyStore{s: a.privacyKeyStore()}, replaces: moved, cancel: cancel},
		})
		must(t, err)
		if report, err := sk.Rewrap(stopped); report.FromFormer != moved || err == nil {
			t.Fatalf("the stopped move = (%+v, %v), want %d keys moved and a stop", report, err, moved)
		}
	}
	must(t, a.Stop(context.Background()))
	return want, total
}

// A move of the data keys from kit-data-key to app-data-key stopped after
// any of its writes leaves every key under one of the two — each replaced
// whole or not at all —, and the next start moves the rest: every card reads
// as it did, the erased person's still erased, and the next pass finds no
// key left under kit-data-key.
func TestAMoveOfTheDataKeysStoppedAnywhereResumes(t *testing.T) {
	needsFileStore(t)
	_, total := formerKeys(t, t.TempDir(), 0)
	if total < 5 {
		t.Fatalf("only %d data keys: the test moves nothing worth stopping", total)
	}
	for moved := range total {
		dir := t.TempDir()
		want, _ := formerKeys(t, dir, moved)
		a, cards := startCards(t, dir)
		if got := cardsOf(t, cards); !slices.Equal(got, want) {
			t.Errorf("stopped after %d: the cards read\n%+v\nwant\n%+v", moved, got, want)
		}
		report, err := a.rewrapNow(t.Context(), false)
		if err != nil || report.FromFormer != 0 || report.Rewrapped != 0 {
			t.Errorf("stopped after %d: a pass after the start = (%+v, %v), want nothing to move", moved, report, err)
		}
		must(t, a.Stop(context.Background()))
	}
}

// A box sealed under data-key itself is sealed under app-data-key and the
// present label alone, which neither kit-data-key nor the former label
// opens; one a former release sealed — under kit-data-key, or under a
// data-key its variable pins, with the former label — still opens.
func TestABoxUnderDataKeyIsSealedUnderThePresentLabelOnly(t *testing.T) {
	needsFileStore(t)
	ctx := t.Context()
	a, _ := startCards(t, t.TempDir())
	defer func() { must(t, a.Stop(context.Background())) }()
	v, err := secret.Random(32)()
	must(t, err)
	_, err = a.secretsNow().kept.Put(ctx, formerSecretPrefix+dataKeySecret, v)
	must(t, err)
	freshSealer(a)
	z, err := a.sealing(ctx)
	must(t, err)
	if z.former == nil {
		t.Fatal("kit-data-key is kept, but the sealer has no former keyring")
	}
	bind := []string{"moved/topic/t", "ref", "/text"}
	box, err := z.seal(ctx, "", []byte(`"now"`), bind...)
	must(t, err)
	_, raw, ok := boxBytes(box)
	if !ok {
		t.Fatalf("not a box: %s", box)
	}
	for name, open := range map[string]func() error{
		"app-data-key, the former label":  func() error { _, err := z.root.Open(ctx, raw, bindAAD(formerSealDomain, bind)); return err },
		"kit-data-key, the present label": func() error { _, err := z.former.Open(ctx, raw, bindAAD(sealDomain, bind)); return err },
		"kit-data-key, the former label":  func() error { _, err := z.former.Open(ctx, raw, bindAAD(formerSealDomain, bind)); return err },
	} {
		if err := open(); !errors.Is(err, secret.SealInvalid) {
			t.Errorf("the box opened under %s: %v", name, err)
		}
	}
	if plain, err := z.root.Open(ctx, raw, bindAAD(sealDomain, bind)); err != nil || string(unpadded(plain)) != `"now"` {
		t.Errorf("the box under the present label: %q %v", plain, err)
	}
	for name, keyring := range map[string]*secret.Keyring{"kit-data-key": z.former, "a pinned data-key": z.root} {
		former, err := keyring.Seal(ctx, padded([]byte(`"then"`)), bindAAD(formerSealDomain, bind))
		must(t, err)
		quoted := []byte(`"` + rootPrefix + base64.RawURLEncoding.EncodeToString(former) + `"`)
		if plain, err := z.open(ctx, quoted, bind...); err != nil || string(plain) != `"then"` {
			t.Errorf("a box a former release sealed under %s: %q %v", name, plain, err)
		}
	}
}

// The former secrets are read until they are copied, and copied once: the
// index key and the SMTP URL under their former names answer while their
// present names hold nothing; a copy makes them present, the SMTP URL again
// only when the former is the newer, and an index key with two values is
// refused, copying nothing.
func TestTheFormerSecretsAreReadUntilCopied(t *testing.T) {
	ctx := t.Context()
	clk := clock.NewManualClock(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC))
	kept := secret.NewMemory(secret.MemoryConfig{Clock: clk})
	for name, value := range map[string]string{"kit-index-key": "a former index key of 32 bytes..", "kit-smtp-url": "smtp://former.test:25"} {
		_, err := kept.Put(ctx, name, secret.FromString(value))
		must(t, err)
	}
	a := NewApp("former")
	a.secrets.Store(&secretStores{kept: kept, from: "memory", frameworkEnv: secret.NewMemory(secret.MemoryConfig{})})
	for name, want := range map[string]string{indexSecret: "a former index key of 32 bytes..", smtpSecret: "smtp://former.test:25"} {
		v, _, err := a.frameworkSecret(ctx, name)
		if err != nil || v.Value.RevealString() != want {
			t.Errorf("%s before the copy: %v %v", name, v.Value.RevealString(), err)
		}
	}
	must(t, a.moveFormerSecrets(ctx, kept))
	for _, name := range []string{"app-index-key", "app-smtp-url"} {
		if vs, err := kept.Versions(ctx, name); err != nil || len(vs) != 1 {
			t.Errorf("%s once copied: %d versions, %v", name, len(vs), err)
		}
	}
	must(t, a.moveFormerSecrets(ctx, kept))
	clk.Advance(time.Hour)
	_, err := kept.Put(ctx, "app-smtp-url", secret.FromString("smtp://present.test:25"))
	must(t, err)
	must(t, a.moveFormerSecrets(ctx, kept))
	if v, _, _ := a.frameworkSecret(ctx, smtpSecret); v.Value.RevealString() != "smtp://present.test:25" {
		t.Errorf("an older former SMTP URL replaced the present one: %s", v.Value.RevealString())
	}
	clk.Advance(time.Hour)
	_, err = kept.Put(ctx, "kit-smtp-url", secret.FromString("smtp://set-by-the-former-release.test:25"))
	must(t, err)
	must(t, a.moveFormerSecrets(ctx, kept))
	if v, _, _ := a.frameworkSecret(ctx, smtpSecret); v.Value.RevealString() != "smtp://set-by-the-former-release.test:25" {
		t.Errorf("a newer former SMTP URL was not copied: %s", v.Value.RevealString())
	}
	_, err = kept.Put(ctx, "kit-index-key", secret.FromString("another index key, of 32 bytes."))
	must(t, err)
	before, _ := kept.Versions(ctx, "app-index-key")
	if err := a.moveFormerSecrets(ctx, kept); !errs.HasCode(err, CodeRenameTarget) {
		t.Errorf("two index keys: %v", err)
	}
	if after, _ := kept.Versions(ctx, "app-index-key"); len(after) != len(before) {
		t.Errorf("a refused copy wrote app-index-key: %d versions, were %d", len(after), len(before))
	}
}

// The framework's prefixes are its own: a secret, a setting or a database of
// the product may take neither the present one nor a former release's.
func TestTheFrameworksSecretPrefixesAreReserved(t *testing.T) {
	for name, want := range map[string]string{
		"app-data-key": "app-", "app-mine": "app-", "kit-smtp-url": "kit-", "kit-mine": "kit-",
		"application": "", "apps": "", "kitchen": "", "stripe-key": "",
	} {
		if got := reservedPrefixOf(name); got != want {
			t.Errorf("reservedPrefixOf(%q) = %q, want %q", name, got, want)
		}
	}
}
