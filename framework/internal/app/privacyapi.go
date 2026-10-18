package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/model"
)

// The Studio's Privacy page, in dev (ADR 0006 §9): the register and its
// gaps, the holds, the journal's latest entries and whether its chain
// holds. It reads only: running a retention, exporting and erasing a person
// are the product's CLI commands (`privacy`, privacycmd.go), run by an
// operator — never an HTTP route (D13, ADR 0147 §8). The route is
// framework/app/studio's (ADR 0180 step 1), which reads privacyView through
// the app's host (studio_host.go). A personal, special or
// secret value never reaches the Studio.

// journalShown is how many of the journal's latest entries the page shows.
const journalShown int = 50

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
