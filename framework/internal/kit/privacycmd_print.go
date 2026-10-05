package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// What the privacy command prints: references, counts and the register —
// never an identity, a value or a key.

// retainNow runs every store's retention once, now, and says what it did.
func (a *App) retainNow(ctx context.Context, mode string, stdout io.Writer) error {
	var failed []error
	for _, st := range a.productStores() {
		if !st.retains() {
			continue
		}
		erased, deleted, err := st.retainOnce(ctx, a, mode)
		failed = append(failed, err)
		if mode == model.RetentionDryRun {
			fmt.Fprintf(stdout, "%s: %d to erase, %d to delete (dry run: journaled, nothing changed)\n", st.base().id, erased, deleted)
			continue
		}
		fmt.Fprintf(stdout, "%s: %d erased, %d deleted\n", st.base().id, erased, deleted)
	}
	return errors.Join(failed...)
}

// printHolds lists the holds, newest first: references only.
func (a *App) printHolds(ctx context.Context, w io.Writer) error {
	holds, err := a.holdList(ctx)
	if err != nil {
		return err
	}
	if len(holds) == 0 {
		fmt.Fprintln(w, "no record is held")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STORE\tRECORD\tSUBJECT\tBY\tAT")
	for _, h := range holds {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Store, h.Record, orDash(h.Subject), h.By, h.At.Format(time.RFC3339))
	}
	return tw.Flush()
}

// printJournal lists the journal, oldest first; with verify, it checks the
// chain and fails where it breaks.
func (a *App) printJournal(ctx context.Context, w io.Writer, verify bool) error {
	if err := a.listJournal(ctx, w); err != nil || !verify {
		return err
	}
	check, err := a.verifyJournal(ctx)
	if err != nil {
		return err
	}
	if check.BrokenAt != 0 {
		fmt.Fprintf(w, "the chain breaks at entry %d: an entry was changed, or one before it removed\n", check.BrokenAt)
		return failure(CodePrivacyJournal, "JOURNAL_BROKEN", fmt.Sprintf("the privacy journal's chain breaks at entry %d", check.BrokenAt), nil)
	}
	fmt.Fprintf(w, "the chain holds: %d entries\n", check.Entries)
	return nil
}

// listJournal prints every entry of the journal, oldest first.
func (a *App) listJournal(ctx context.Context, w io.Writer) error {
	entries, err := a.journalEntries(ctx, 1<<30)
	if err != nil {
		return err
	}
	slices.Reverse(entries)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tAT\tOP\tSTORE\tRECORD\tSUBJECT\tBY")
	for _, e := range entries {
		op := e.Op
		if e.DryRun {
			op = e.Op + " (dry run)"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Seq, e.At.Format(time.RFC3339), op, orDash(e.Store), orDash(e.Record), orDash(e.Subject), e.By)
	}
	return tw.Flush()
}

// printErasure says what an erasure did, store by store.
func printErasure(w io.Writer, e Erasure) {
	if len(e.Stores) == 0 {
		fmt.Fprintln(w, "no record of these identities")
		return
	}
	for _, s := range e.Stores {
		fmt.Fprintf(w, "%s: %d erased, %d deleted, %d held\n", s.Store, len(s.Erased), len(s.Deleted), len(s.Held))
	}
}

// writeIndented writes v as indented JSON.
func writeIndented(w io.Writer, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// orDash is s, or "-" for nothing.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
