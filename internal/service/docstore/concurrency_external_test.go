// Package docstore_test — readers against a writer that waits on the disk,
// and every call at once under the race detector.
package docstore_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/docstore"
)

// TestReadersDoNotWaitForTheDisk pins the two-lock design: a writer holds
// only the writers' lock while its publication waits on the device, so every
// read answers meanwhile — with the state before the write, never half of it
// — and the write appears once it is durable.
//
// Goroutine lifecycle: Open, the held Put and Close's drain each run on a
// goroutine of their own. The first two end once the gate releases them and
// hand their result back on a channel the test reads; the drain ranges over
// the gate's announcements and ends when the test closes that channel.
func TestReadersDoNotWaitForTheDisk(t *testing.T) {
	t.Parallel()
	gated := &gatedFS{FullFS: memFS(), entered: make(chan string, 1), release: make(chan struct{})}
	cfg := accountConfig(gated)
	cfg.FoldAt = -1
	//: Open publishes its first snapshot through the gate: let it through.
	opened := make(chan *docstore.Store[account], 1)
	go func() {
		store, err := openWith(cfg)
		if err != nil {
			t.Errorf("Open() = %v", err)
		}
		opened <- store
	}()
	<-gated.entered
	gated.release <- struct{}{}
	store := <-opened
	if store == nil {
		t.FailNow()
	}
	//: from here the gate holds each publication until released.
	gated.release = make(chan struct{})

	writeDone := make(chan error, 1)
	go func() { writeDone <- store.Put(account{ID: "acc_1", Email: "held@x.dev"}) }()
	<-gated.entered // the write is inside the device wait now

	//: every read answers while the writer waits, with the state before it.
	if _, err := store.Get("acc_1"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("Get while the write waits = %v, want DocumentNotFound", err)
	}
	if all, err := store.List(); err != nil || len(all) != 0 {
		t.Fatalf("List while the write waits = %v, %v", all, err)
	}
	if _, err := store.Lookup("email", "held@x.dev"); !errs.HasCode(err, docstore.CodeDocumentNotFound) {
		t.Fatalf("Lookup while the write waits = %v", err)
	}
	if stats := store.Stats(); stats.Documents != 0 {
		t.Fatalf("Stats while the write waits = %+v", stats)
	}

	close(gated.release)
	must(t, <-writeDone)
	if a, err := store.Lookup("email", "held@x.dev"); err != nil || a.ID != "acc_1" {
		t.Fatalf("after the write, Lookup = %+v, %v", a, err)
	}
	//: Close publishes the snapshot through the open gate.
	go func() {
		for range gated.entered {
		}
	}()
	must(t, store.Close())
	close(gated.entered)
}

// TestEverythingAtOnce runs every call concurrently against one store, in
// memory and over a filesystem, for the race detector: writers on distinct
// and shared keys, updates, deletions, and every kind of read.
func TestEverythingAtOnce(t *testing.T) {
	t.Parallel()
	for _, backend := range []struct {
		name string
		open func(t *testing.T) *docstore.Store[account]
	}{
		{"memory", func(t *testing.T) *docstore.Store[account] { return openAccounts(t, nil) }},
		{"filesystem", func(t *testing.T) *docstore.Store[account] {
			cfg := accountConfig(memFS())
			cfg.FoldAt = 16
			store, err := openWith(cfg)
			must(t, err)
			t.Cleanup(func() { must(t, store.Close()) })
			return store
		}},
	} {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			store := backend.open(t)
			store.OnWrite(func(key string) {
				_, getErr := store.Get(key)
				tolerate(t, getErr, docstore.CodeDocumentNotFound)
			})
			var wg sync.WaitGroup
			for worker := range 8 {
				wg.Go(func() { churn(t, store, worker) })
			}
			wg.Wait()
			//: whatever the interleaving, every index agrees with the documents.
			all, err := store.List()
			must(t, err)
			for _, a := range all {
				if a.Email == "" {
					continue
				}
				if got, lookupErr := store.Lookup("email", a.Email); lookupErr != nil || got.ID != a.ID {
					t.Fatalf("the index disagrees with %s: %+v, %v", a.ID, got, lookupErr)
				}
			}
		})
	}
}

// churn is one worker of TestEverythingAtOnce: every call, fifty times, on
// its own keys and on one key every worker shares. An error is only accepted
// when the interleaving can produce it: another worker deleted the key, or
// took the unique e-mail first.
func churn(t *testing.T, store *docstore.Store[account], worker int) {
	for i := range 50 {
		key := fmt.Sprintf("acc_%d_%d", worker, i%5)
		team := fmt.Sprintf("team_%d", i%3)
		tolerate(t, store.Put(account{ID: key, Email: key + "@x.dev", Teams: []string{team}}))
		_, updateErr := store.Update(key, func(a *account) error { a.Name = "n"; return nil })
		tolerate(t, updateErr)
		_, getErr := store.Get(key)
		tolerate(t, getErr)
		_, lookupErr := store.Lookup("email", key+"@x.dev")
		tolerate(t, lookupErr)
		_, findErr := store.Find("team", team)
		tolerate(t, findErr)
		_, listErr := store.List()
		tolerate(t, listErr)
		if stats := store.Stats(); stats.Documents < 0 {
			t.Errorf("Stats() = %+v", stats)
		}
		if i%7 == 0 {
			tolerate(t, store.Delete(key))
		}
		//: a key every worker fights over, with an e-mail of its own.
		tolerate(t, store.Put(account{ID: "shared", Email: fmt.Sprintf("shared-%d@x.dev", worker)}))
	}
}

// tolerate fails the test unless err is nil or carries one of codes.
func tolerate(t *testing.T, err error, codes ...errs.Code) {
	t.Helper()
	if err == nil {
		return
	}
	for _, code := range codes {
		if errs.HasCode(err, code) {
			return
		}
	}
	t.Errorf("unexpected %v", err)
}
