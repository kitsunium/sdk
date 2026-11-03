package storetest

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
)

// keyedMemos is how many memos the records-keys case lists: more than one
// chunk of the keys a SQL store reads per call (256, storeengine/sql.go).
const keyedMemos int = 300

// listKey is the memo whose notice makes the watch list its store's keys.
const listKey string = "list"

// memo is what the cases' watch hears: its text is a field a moderation
// reads (app.Moderated).
type memo struct {
	ID   string `json:"id"`
	Text string `json:"text" app:"moderated"`
}

// key is the memo's key.
func (m memo) key() string { return m.ID }

// listing is what the memos' watch heard, and the keys it listed when told
// to.
type listing struct {
	sync.Mutex
	heard []string
	keys  []string
	err   error
}

// notice keeps the key a notice named.
func (l *listing) notice(key string) {
	l.Lock()
	defer l.Unlock()
	l.heard = append(l.heard, key)
}

// has reports whether a notice named key.
func (l *listing) has(key string) bool {
	l.Lock()
	defer l.Unlock()
	return slices.Contains(l.heard, key)
}

// listed returns the keys the watch listed, and the failure that ended the
// listing.
func (l *listing) listed() ([]string, error) {
	l.Lock()
	defer l.Unlock()
	return slices.Clone(l.keys), l.err
}

// declareMemoStore declares the memos, with no watch over them.
func declareMemoStore(svc *app.Service, s *suite) {
	s.listed = &listing{}
	s.memos = svc.Store("memos", memo.key)
}

// declareMemos declares the memos and a watch over the service's own
// stores: it keeps the key every notice names, and, told of listKey, lists
// its store's keys through the records port first.
func declareMemos(svc *app.Service, s *suite) {
	declareMemoStore(svc, s)
	svc.Watch("screen", app.Moderated, s.listed.screen, app.OwnStores())
}

// screen keeps the key a notice names; told of listKey, it lists the
// store's keys first, and keeps the key once the listing is done.
func (l *listing) screen(ctx context.Context, w app.Written) error {
	defer l.notice(w.Key)
	if w.Key != listKey {
		return nil
	}
	recs, err := app.RecordsOf(ctx, w.Store)
	if err != nil {
		return err
	}
	var keys []string
	for key, keyErr := range recs.Keys(ctx) {
		if keyErr != nil {
			err = keyErr
			break
		}
		keys = append(keys, key)
	}
	l.Lock()
	defer l.Unlock()
	l.keys, l.err = keys, err
	return nil
}

// settle writes the memo id and waits for its notice: one watch hears its
// notices in the order they were queued, so whatever was queued before it
// was heard.
func (s *suite) settle(t *testing.T, id string) {
	t.Helper()
	must(t, s.memos.Insert(t.Context(), memo{ID: id, Text: "settle"}))
	eventually(t, id+" heard", func() bool { return s.listed.has(id) })
}

// recordsKeys checks Records.Keys on the backend (ADR 0178 C6) over more
// keys than one chunk reads: a watch lists the keyedMemos memos of its store
// — and the one that asked —, in key order, each once, under the database's
// timeout.
func recordsKeys(t *testing.T, s *suite) {
	want := []string{listKey}
	must(t, app.Transact(t.Context(), func(ctx context.Context) error {
		for i := range keyedMemos {
			id := "m" + strconv.Itoa(1000 + i)[1:]
			want = append(want, id)
			if err := s.memos.Insert(ctx, memo{ID: id, Text: "listed"}); err != nil {
				return err
			}
		}
		return nil
	}))
	s.settle(t, listKey)
	keys, err := s.listed.listed()
	slices.Sort(want)
	if err != nil || !slices.Equal(keys, want) {
		t.Errorf("Keys listed %d keys (%v), want the %d memos in key order", len(keys), err, len(want))
	}
}

// declareOutbox declares a mailer: on a database its outbox is a table there
// (ADR 0178 C7), elsewhere the spool's queue; in dev, with no relay, the
// capture transport delivers.
func declareOutbox(svc *app.Service, s *suite) {
	s.mailer = svc.Mailer("notify", app.From("Storetest", "storetest@example.com"), app.MailAttempts(1))
}

// letterAbout is the mail the outbox case sends about id.
func letterAbout(id string) mail.Message {
	return mail.Message{To: []mail.Address{{Addr: "reader@example.com"}}, Subject: "about " + id, Text: "A mail of the store conformance suite."}
}

// mailed reports whether the capture transport kept the mail about id.
func (s *suite) mailed(id string) bool {
	for _, m := range s.mailer.Captured() {
		if m.Subject == "about "+id {
			return true
		}
	}
	return false
}

// outbox checks a mailer's outbox on the backend: a mail sent in a
// transaction that rolls back never is delivered, one sent in a transaction
// that commits is; on a database the outbox is its table.
func outbox(t *testing.T, s *suite) {
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		_, sendErr := s.mailer.Send(ctx, letterAbout("dropped"))
		return errors.Join(sendErr, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	must(t, app.Transact(ctx, func(ctx context.Context) error {
		_, sendErr := s.mailer.Send(ctx, letterAbout("kept"))
		return sendErr
	}))
	eventually(t, "the committed mail", func() bool { return s.mailed("kept") })
	if s.mailed("dropped") {
		t.Error("a mail sent in a transaction that rolled back was delivered")
	}
	if node := s.running.Graph().Node("st-outbox/mailer/notify"); s.onDatabase() && (node == nil || node.Mailer.Outbox != "sql") {
		t.Errorf("the outbox on a database: %+v, want its table", node)
	}
}
