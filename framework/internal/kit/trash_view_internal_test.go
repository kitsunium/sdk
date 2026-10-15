package kit

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The Studio's trash list refuses, with CodeTrashList, a store the app does
// not have — a NotFound — and one without revisions, which keeps no trash —
// an Invalid —, each naming the store.

type binnedNote struct {
	ID string `json:"id"`
}

var (
	binned      = NewService("binned", "Stores for the trash list's refusals.")
	binnedKept  = binned.Store("kept", func(n binnedNote) string { return n.ID }, Revisions(2))
	binnedPlain = binned.Store("plain", func(n binnedNote) string { return n.ID })
)

func TestTheTrashListIsRefusedWithATypedCode(t *testing.T) {
	app := NewApp("binned", binned).With(InMemory(), Env(EnvDev))
	must(t, app.resolve())
	got, err := app.trashOf(binnedKept.id)
	if err != nil || got != trashSource(binnedKept) {
		t.Fatalf("trashOf(%s) = %v, %v; want the store", binnedKept.id, got, err)
	}
	for _, c := range []struct{ id, wire, public string }{
		{"binned/store/nothing", WireNotFound, "no such store: binned/store/nothing"},
		{binnedPlain.id, WireInvalid, "binned/store/plain keeps no trash: declare it with kit.Revisions(n)"},
	} {
		_, err := app.trashOf(c.id)
		var ke *Error
		if !errs.HasCode(err, CodeTrashList) || !errors.As(err, &ke) || ke.Code != c.wire || ke.Message != c.public {
			t.Errorf("trashOf(%s) = %v; want a %s CodeTrashList saying %q", c.id, err, c.wire, c.public)
		}
	}
}
