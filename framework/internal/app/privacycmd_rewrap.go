package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// `privacy rewrap` (ADR 0180 step 5b, Q6): every data key wrapped under
// kit-data-key — what a former release made — moved under app-data-key now,
// as the start does, and what the pass did said. `privacy rewrap -back` is
// the way back to the release before step 5b: every data key moved back
// under kit-data-key's newest version, which that release opens, before it
// starts. A message sealed under data-key itself since this release started
// is bound to app-data-key and to the present label, which that release
// reads as empty: drain the queues first.

// rewrapCommand runs `privacy rewrap [-back]`.
func (a *App) rewrapCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("privacy rewrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	back := fs.Bool("back", false, "move every data key back under the data-key a former release kept, before it starts")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "usage: %s privacy rewrap [-back]\n", a.name)
		return 2
	}
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		report, err := a.rewrapNow(ctx, *back)
		if err != nil {
			return err
		}
		to := ownSecretPrefix + dataKeySecret
		if *back {
			to = formerSecretPrefix + dataKeySecret
		}
		fmt.Fprintf(stdout, "%d data keys moved from the other data-key, %d to %s's version %d, %d already there\n",
			report.FromFormer, report.Rewrapped-report.FromFormer, to, report.Root, report.Current)
		if *back {
			fmt.Fprintln(stdout, "a message sealed under data-key itself since this release started opens only under "+ownSecretPrefix+dataKeySecret+": the former release reads it as empty")
		}
		return nil
	})
}

// rewrapNow runs one re-wrap pass over the framework's data keys: to the
// present data-key, or, back, to the one a former release kept.
func (a *App) rewrapNow(ctx context.Context, back bool) (secret.RewrapReport, error) {
	keys := a.privacyKeyStore()
	if keys == nil {
		return secret.RewrapReport{}, Invalid("the product keeps no data keys: there is nothing to re-wrap")
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return secret.RewrapReport{}, err
	}
	root, former := z.root, z.former
	if back {
		if former == nil {
			return secret.RewrapReport{}, Invalid("no data-key a former release kept is in the secret store: there is nothing to go back to")
		}
		root, former = former, root
	}
	sk, err := secret.NewSubjectKeys(secret.SubjectKeysConfig{Root: root, Former: former, Store: keyStore{s: keys}})
	if err != nil {
		return secret.RewrapReport{}, failure(CodeSealKey, "SEAL_KEYS", "the framework's data keys could not be set up", err)
	}
	report, err := sk.Rewrap(ctx)
	if err != nil && !errors.Is(err, secret.SubjectKeyUnreadable) {
		return report, failure(CodeSealRewrap, "SEAL_REWRAP", "the framework's data keys could not all be re-wrapped", err)
	}
	if err != nil {
		return report, failure(CodeSealRewrap, "SEAL_REWRAP", "some of the framework's data keys do not unwrap under either data-key: they are left as they are", err)
	}
	return report, nil
}
