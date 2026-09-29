// Package kit — the Studio's privacy pages: the journal, the holds, the
// register.
package kit

import (
	"context"
	"net/http"

	"github.com/kitsunium/sdk/framework/model"
)

// The Studio's Privacy page, in dev (ADR 0006 §9): the register and its
// gaps, the holds, the journal's latest entries and whether its chain
// holds. It reads only: running a retention, exporting and erasing a person
// are the product's CLI commands (`privacy`, privacycmd.go), run by an
// operator — never an HTTP route (D13, ADR 0143 §8). A personal, special or
// secret value never reaches the Studio.

// journalShown is how many of the journal's latest entries the page shows.
const journalShown int = 50

// mountPrivacyTools registers the Privacy page's routes through api, behind
// the Studio's guard.
func (a *App) mountPrivacyTools(api func(pattern string, h http.HandlerFunc)) {
	api("GET /_kit/api/privacy", a.servePrivacy)
}

// servePrivacy answers GET /_kit/api/privacy: a journal or holds kit cannot
// read are an error, never an empty page.
func (a *App) servePrivacy(w http.ResponseWriter, r *http.Request) {
	view, err := a.privacyView(r.Context())
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// privacyView is what the Privacy page shows.
func (a *App) privacyView(ctx context.Context) (model.Privacy, error) {
	check, err := a.verifyJournal(ctx)
	if err != nil {
		return model.Privacy{}, err
	}
	holds, err := a.holdList(ctx)
	if err != nil {
		return model.Privacy{}, err
	}
	journal, err := a.journalEntries(ctx, journalShown)
	if err != nil {
		return model.Privacy{}, err
	}
	return model.Privacy{Retention: a.retentionMode(), Register: a.register(a.Graph()), Holds: holds, Journal: journal, Chain: check}, nil
}
