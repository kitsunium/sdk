package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What `watch replay` says: how many notices it queued and from how many
// stores on its standard output, and on a name that is no watch a NotFound
// refusal with CodeWatchReplay that lists the product's watches.

// replayCmd runs the watch command on an app of w's place that is not
// running, and returns its status and streams.
func replayCmd(t *testing.T, w *keysWorld, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = w.app(nil).watchCommand(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestTheWatchCommandSaysWhatItQueued(t *testing.T) {
	place := keysEngines(t)["file"]
	w := newKeysWorld(t, place)
	first := w.start(t, nil)
	for _, id := range []string{"p1", "p2", "p3"} {
		must(t, w.posts.Insert(t.Context(), keysPost{ID: id, Author: "ann", Body: "hello"}))
	}
	stop(t, first)

	code, out, errOut := replayCmd(t, w, "replay", w.watch.id)
	want := say("watch.replayed", "watch", w.watch.id, "notices", 3, "stores", 1).String() + "\n"
	if code != 0 || out != want {
		t.Fatalf("watch replay = %d %q %q; want 0 %q", code, out, errOut, want)
	}

	code, out, errOut = replayCmd(t, w, "replay", "screening/subscription/nothing")
	if code != 1 || out != "" || !strings.Contains(errOut, "no such watch: screening/subscription/nothing") ||
		!strings.Contains(errOut, "the product's watches: "+w.watch.id) {
		t.Errorf("watch replay of no watch = %d %q %q; want 1, the refusal and the product's watches", code, out, errOut)
	}
	if code, _, errOut := replayCmd(t, w); code != 2 || !strings.Contains(errOut, "forum watch replay WATCH") {
		t.Errorf("watch without arguments = %d %q; want 2 and the usage", code, errOut)
	}
}

func TestAWatchThatIsNotDeclaredIsRefusedByName(t *testing.T) {
	w := newKeysWorld(t, keysEngines(t)["file"])
	app := w.app(nil)
	must(t, app.resolve())
	if got, err := app.watchNamed(w.watch.id, &bytes.Buffer{}); err != nil || got != w.watch {
		t.Fatalf("watchNamed(%s) = %v, %v; want the watch", w.watch.id, got, err)
	}
	_, err := app.watchNamed(w.posts.id, &bytes.Buffer{})
	var ke *Error
	if !errs.HasCode(err, CodeWatchReplay) || !errors.As(err, &ke) || ke.Code != WireNotFound ||
		errs.PublicOf(err) != "no such watch: "+w.posts.id {
		t.Errorf("watchNamed of a store = %v (public %q); want a NotFound CodeWatchReplay naming it", err, errs.PublicOf(err))
	}
}
