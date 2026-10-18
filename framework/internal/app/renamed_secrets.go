package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The secrets and the sealing label, re-sealed (ADR 0180 step 5b, Q5, Q6).
// The framework keeps its own secrets in the environment's store under its
// name — app-data-key, app-index-key, app-smtp-url —, and a former release
// kept them under the tool's, kit-…. Two of them are values whose name binds
// nothing: index-key and smtp-url are copied, once, the first time this
// release opens the store (moveFormerSecrets), and read under their former
// name while the copy is not made. The third is a keyring, and its name is
// bound into everything it wraps (the SDK's Keyring.binding): data-key is a
// new secret, generated under its new name, and every data key wrapped under
// kit-data-key is re-wrapped under it by the start's re-wrap pass
// (rewrapDataKeys), one compare-and-swap per key, which secret.SubjectKeys
// does once it is told the root it replaces (SubjectKeysConfig.Former). A
// message sealed under data-key itself is read under the label it was
// sealed with — the former label kit/v1 sealed at rest, under kit-data-key
// or under data-key pinned by its variable, whose name did not change — and
// sealed under the present one alone.
//
// An erasure destroys the one wrapped key its subject has, under whichever
// root wraps it, and folds the store of data keys: in what the product
// keeps, nothing is left that either root opens. What it cannot reach — a
// backup of that store, a database's own history of its rows — holds a
// wrapped copy that opens only while the version that wrapped it is kept,
// the bound ADR 0142 sets. kit-data-key no longer rotates, so its versions
// are retired on the schedule its rotation would have pruned them on, once
// nothing is left under them (retireFormerDataKey): the same bound.
//
// Nothing else a former release kept is written, replaced or deleted: until
// kit-data-key's retirement the release before this one finds its secrets
// where it left them, and after it a fresh version under the same name;
// what it does not open is a data key this one re-wrapped, which
// `privacy rewrap -back` moves back under kit-data-key before it starts.

const (
	// ownSecretPrefix names the framework's own secrets in the environment's
	// store, so that no product secret can take one's place.
	ownSecretPrefix = frameworkName + "-"
	// formerSecretPrefix is the prefix a former release kept them under:
	// read, never written, and reserved, for two minors (Q12).
	formerSecretPrefix = formerName + "-"
	// sealDomain separates what the framework binds a box under data-key
	// itself to.
	sealDomain = frameworkName + "/v1 sealed at rest\x00"
	// formerSealDomain is the label a former release bound such a box to:
	// read, never written, for two minors (Q12).
	formerSealDomain = formerName + "/v1 sealed at rest\x00"
)

// copiedSecrets are the framework's own secrets a value is copied for: their
// name binds nothing. data-key is not among them: what it wraps is re-wrapped
// under a new secret, never copied.
var copiedSecrets = []string{indexSecret, smtpSecret}

// reservedPrefixOf is the prefix of the framework's own secrets name starts
// with — its present one, or a former release's while what it kept may be
// met —, "" for none: no secret, setting or database of the product may take
// one, since its name would be read where the framework's are kept.
func reservedPrefixOf(name string) string {
	for _, prefix := range []string{ownSecretPrefix, formerSecretPrefix} {
		if strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	return ""
}

// moveFormerSecrets copies, in the store the environment keeps, each of the
// framework's own secrets a former release kept under its former name to its
// present one, once: index-key when the present name holds none, smtp-url
// when it holds none or an older version than the former — an operator who
// went back to the former release and set it there set the newer one. Both
// are judged before either is written, so that a refusal copies nothing: an
// index key kept under both names with two values is CodeRenameTarget, since
// every reference the framework keeps was made under one of them, and which
// is the operator's to say. A copy the store refuses is CodeRenameFailed,
// and the former stays where it was, read until the next start copies it.
func (a *App) moveFormerSecrets(ctx context.Context, kept secret.Store) error {
	if kept == nil {
		return nil
	}
	var copies []string
	for _, name := range copiedSecrets {
		needed, err := copyNeeded(ctx, kept, name)
		if err != nil {
			return err
		}
		if needed {
			copies = append(copies, name)
		}
	}
	for _, name := range copies {
		if err := copyFormer(ctx, kept, name); err != nil {
			return err
		}
		if a.log != nil {
			logger.Info(ctx, a.log, "a secret a former release kept was copied to its present name (ADR 0180 step 5b)",
				logger.String("from", formerSecretPrefix+name), logger.String("to", ownSecretPrefix+name))
		}
	}
	return nil
}

// copyNeeded reports whether the former secret name holds a value its
// present name does not hold yet.
func copyNeeded(ctx context.Context, kept secret.Store, name string) (bool, error) {
	from, to := formerSecretPrefix+name, ownSecretPrefix+name
	former, err := kept.Get(ctx, from)
	switch {
	case errors.Is(err, secret.NotFound):
		return false, nil
	case err != nil:
		return false, renameFailed(from, to, err)
	}
	formerValue := former.Value.Reveal()
	defer clear(formerValue)
	present, err := kept.Get(ctx, to)
	switch {
	case errors.Is(err, secret.NotFound):
		return true, nil
	case err != nil:
		return false, renameFailed(from, to, err)
	}
	presentValue := present.Value.Reveal()
	defer clear(presentValue)
	same := bytes.Equal(formerValue, presentValue)
	switch {
	case same:
		return false, nil
	case name == indexSecret:
		return false, renameTaken(from, to)
	}
	return former.Created.After(present.Created), nil
}

// copyFormer writes the former secret name's current value as a new version
// of its present name.
func copyFormer(ctx context.Context, kept secret.Store, name string) error {
	from, to := formerSecretPrefix+name, ownSecretPrefix+name
	former, err := kept.Get(ctx, from)
	if err == nil {
		_, err = kept.Put(ctx, to, former.Value)
	}
	if err != nil {
		return renameFailed(from, to, err)
	}
	return nil
}

// formerDataKeyring is the keyring over kit-data-key in the store the
// environment keeps, which wrapped the data keys a former release made; nil
// when the store keeps none — a product that never sealed under a former
// release, or whose data-key its variable pins, under a name that did not
// change.
func (a *App) formerDataKeyring(ctx context.Context) (*secret.Keyring, error) {
	st := a.secretsNow()
	if st.kept == nil {
		return nil, nil
	}
	name := formerSecretPrefix + dataKeySecret
	switch _, err := st.kept.Versions(ctx, name); {
	case errors.Is(err, secret.NotFound):
		return nil, nil
	case err != nil:
		return nil, failure(CodeSealKey, "SEAL_FORMER_KEY", "the data-key a former release kept could not be read", err)
	}
	keyring, err := secret.NewKeyring(st.kept, name)
	if err != nil {
		return nil, failure(CodeSealKey, "SEAL_FORMER_KEY", "the data-key a former release kept could not be read", err)
	}
	return keyring, nil
}

// retireFormerDataKey destroys what kit-data-key's versions hold once its
// rotation would have pruned them: when its newest version is older than
// data-key's Keep times its RotateEvery — the instant data-key's own rotation
// prunes a version — and a re-wrap pass that read it found no data key
// under it and none it could not open. The store offers no deletion, so a
// fresh random version is written under the name and every other version
// pruned: what a former release wrapped under them — a wrapped key a backup
// or a database's own history kept of an erased person, a box under
// data-key itself older than that — stops opening, as it would have under
// the former release's own rotation (ADR 0142: an erasure is complete
// everywhere once the version that wrapped the key is pruned). Nothing the
// running product still needs is under it: every data key moved, and the
// way back (`privacy rewrap -back`) wraps under the fresh version, which the
// former release opens. It runs after every clean pass, so a fresh version
// that wrapped nothing is replaced again a Keep×RotateEvery later.
func (a *App) retireFormerDataKey(ctx context.Context, z *sealer, report secret.RewrapReport) {
	kept := a.secretsNow().kept
	if z.former == nil || kept == nil || report.FromFormer > 0 || report.Unreadable > 0 {
		return
	}
	name := formerSecretPrefix + dataKeySecret
	retired, due := a.formerRetirementDue(ctx, kept, name)
	if !due {
		return
	}
	fresh, err := secret.Random(dataKeyBytes)()
	if err == nil {
		_, err = kept.Put(ctx, name, fresh)
	}
	if err == nil {
		err = kept.Prune(ctx, name, 1)
	}
	if err != nil {
		logger.Warn(ctx, a.log, "the data-key a former release kept could not be retired: the next clean re-wrap tries again",
			logger.String("secret", name), logger.String("error", errs.PublicOf(err)))
		return
	}
	logger.Info(ctx, a.log, "the versions of the data-key a former release kept are retired: nothing they wrapped opens any more (ADR 0180 step 5b)",
		logger.String("secret", name), logger.Int("retired", retired))
}

// formerRetirementDue reports whether the former data-key name in kept is due
// for retirement — its newest version older than data-key's Keep times its
// RotateEvery, the instant data-key's own rotation prunes a version —, and
// how many versions it holds. A version without its instant is never judged
// due.
func (a *App) formerRetirementDue(ctx context.Context, kept secret.Store, name string) (int, bool) {
	a.privacy.mu.Lock()
	sec := a.privacy.dataKey
	a.privacy.mu.Unlock()
	if sec == nil {
		return 0, false
	}
	versions, err := kept.Versions(ctx, name)
	if err != nil || len(versions) == 0 || versions[0].Created.IsZero() {
		return 0, false
	}
	bound := versions[0].Created.Add(time.Duration(sec.opts.keep) * sec.opts.every)
	return len(versions), !a.clock.Now().Before(bound)
}

// formerOwnSecret is the present name of one of the framework's own secrets
// name spells under its former prefix, among the names the secrets command
// manages; false when name is no such spelling.
func formerOwnSecret(all []managedSecret, name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, formerSecretPrefix)
	if !ok {
		return "", false
	}
	present := ownSecretPrefix + rest
	return present, slices.ContainsFunc(all, func(m managedSecret) bool { return m.name == present })
}
