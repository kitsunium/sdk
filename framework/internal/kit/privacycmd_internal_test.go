package kit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// letter is what the privacy command's product keeps.
type letter struct {
	ID   string     `json:"id"`
	To   string     `json:"to" kit:"subject"`
	Body string     `json:"body" kit:"personal"`
	Sent *time.Time `json:"sent,omitempty"`
}

func (l letter) sent() (time.Time, bool) {
	if l.Sent == nil {
		return time.Time{}, false
	}
	return *l.Sent, true
}

var cmdPrivacy = NewService("cmd-privacy", "Letters, for the privacy command's tests.")

var cmdLetters = cmdPrivacy.Store("letters", func(l letter) string { return l.ID },
	EraseAfter(time.Hour, letter.sent), Purpose("Write letters"))

// privacyCmd runs the privacy command of an app named "post" on the data
// in dir, as an operator would, the product stopped.
func privacyCmd(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	app := NewApp("post", cmdPrivacy).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:1"), Logs(io.Discard))
	var out, errOut bytes.Buffer
	code = app.privacyCommand(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// privacyStep is one run of the privacy command, and what it must answer.
type privacyStep struct {
	args []string
	code int
	// out is the whole of stdout, when it is set; has and hasNot are what
	// stdout says and never says; errHas is what stderr says.
	out         string
	has, hasNot []string
	errHas      string
}

// run runs the step on the data in dir, and checks its answer.
func (s privacyStep) run(t *testing.T, dir string) {
	t.Helper()
	code, out, errOut := privacyCmd(t, dir, s.args...)
	if code != s.code {
		t.Fatalf("privacy %v: exit %d, want %d\n%s%s", s.args, code, s.code, out, errOut)
	}
	if s.out != "" && out != s.out {
		t.Errorf("privacy %v: %q, want %q", s.args, out, s.out)
	}
	for _, w := range s.has {
		if !strings.Contains(out, w) {
			t.Errorf("privacy %v does not say %q:\n%s", s.args, w, out)
		}
	}
	for _, w := range s.hasNot {
		if strings.Contains(out, w) {
			t.Errorf("privacy %v says %q:\n%s", s.args, w, out)
		}
	}
	if !strings.Contains(errOut, s.errHas) {
		t.Errorf("privacy %v: stderr %q, want %q", s.args, errOut, s.errHas)
	}
}

// lettersIn runs the letters' product on dir without its retention — which
// would erase at its start what is due —, writes a letter to ann and a sent
// one to bob, and stops it: the command's retention finds bob's due.
func lettersIn(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("KIT_RETENTION", "off")
	app := NewApp("post", cmdPrivacy).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:0"), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	sent := time.Now().Add(-2 * time.Hour)
	for _, l := range []letter{{ID: "l1", To: "ann@x.dev", Body: "dear Ann"}, {ID: "l2", To: "bob@x.dev", Body: "dear Bob", Sent: &sent}} {
		if err := cmdLetters.Insert(t.Context(), l); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIT_RETENTION", "on")
}

func TestThePrivacyCommand(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
	dir := t.TempDir()
	lettersIn(t, dir)
	for _, step := range []privacyStep{
		{args: []string{"register"}, has: []string{"cmd-privacy/store/letters", "Write letters", "1h0m0s after letter.sent", "(a) the controller"}},
		{args: []string{"export", "ann@x.dev"}, has: []string{"dear Ann"}, hasNot: []string{"dear Bob"}},
		{args: []string{"erase", "ann@x.dev"}, code: 1, errHas: "-reason"},
		{args: []string{"erase", "-reason", "asked by letter", "ann@x.dev"}, out: "cmd-privacy/store/letters: 1 erased, 0 deleted, 0 held\n"},
		{args: []string{"retention", "-dry-run"}, out: "cmd-privacy/store/letters: 1 to erase, 0 to delete (dry run: journaled, nothing changed)\n"},
		{args: []string{"retention"}, out: "cmd-privacy/store/letters: 1 erased, 0 deleted\n"},
		{args: []string{"journal", "-verify"}, has: []string{"the chain holds: 4 entries", "cli"}, hasNot: []string{"ann@x.dev"}},
		{args: []string{"holds"}, out: "no record is held\n"},
		{args: []string{"seal", "cmd-privacy/store/letters"}, code: 1, errHas: "step 3"},
		{code: 2},
	} {
		step.run(t, dir)
	}
}

// A store belongs to one process: the command refuses while the product
// answers on its address.
//
// Goroutine lifecycle: one goroutine serves until the deferred Close; it
// reports on a buffered channel, which the test drains.
func TestThePrivacyCommandLeavesARunningProductAlone(t *testing.T) {
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		ReadHeaderTimeout: time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	}()
	defer func() {
		if err := srv.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	app := NewApp("post", cmdPrivacy).With(DataDir(t.TempDir()), Env(EnvProduction), Listen(ln.Addr().String()), Logs(io.Discard))
	var out, errOut bytes.Buffer
	if code := app.privacyCommand(t.Context(), []string{"holds"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "stop it first") {
		t.Errorf("holds while the product runs: %d %q", code, errOut.String())
	}
}

// When kit cannot tell whether a record is held — its holds cannot be read
// —, nothing is deleted; a journal kit cannot read is an error for the
// Privacy page, never an empty journal.
func TestWhatKitCannotReadStopsIt(t *testing.T) {
	app := NewApp("post", cmdPrivacy).With(InMemory(), Env(EnvDev), Listen("127.0.0.1:0"), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	if err := cmdLetters.Insert(t.Context(), letter{ID: "l1", To: "ann@x.dev", Body: "dear Ann"}); err != nil {
		t.Fatal(err)
	}
	holds, journal := app.privacyStores()
	if err := holds.stop(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	if err := cmdLetters.Delete(t.Context(), "l1"); err == nil || isConflict(err) {
		t.Errorf("a deletion kit cannot check = %v", err)
	}
	if _, err := cmdLetters.Get(t.Context(), "l1"); err != nil {
		t.Errorf("the letter is gone: %v", err)
	}
	if err := journal.stop(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	if _, err := app.privacyView(t.Context()); err == nil {
		t.Error("the Privacy page reads a journal kit cannot read")
	}
}
