// Package secret — the file store: a directory the store owns, one record per
// secret published atomically through vfs, writers serialised across
// processes through the lock domain, and optional sealing at rest.
package secret

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelock "github.com/kitsunium/sdk/internal/core/lock"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// lockPrefix namespaces the lock names this store takes, one per secret, so a
// locker shared with other code cannot collide with them.
const lockPrefix string = "kitsunium/secret/"

// fileStore keeps each secret's history in one record under a directory it
// owns. It holds the directory open for its lifetime and implements io.Closer
// as an ADR 0039 sibling of the Store port.
type fileStore struct {
	// root is the directory, confined: every path resolves inside it.
	root corevfs.FullFS
	// closer releases root's descriptor.
	closer io.Closer
	// locker serialises writers of one secret, across goroutines AND
	// processes, because a Put is a read-modify-write of the record.
	locker corelock.Locker
	// key seals records when sealed is set.
	key corecrypto.Key
	// sealed records whether a key was configured.
	sealed bool
	// clk stamps Created.
	clk clock.Clock
}

// NewFile returns a Store that keeps its secrets in cfg.Dir.
//
// Every write — a Put, a Prune — publishes the secret's whole record through
// vfs.WriteAtomic, so a reader, in this process or another, sees the history
// before the write or after it and never a torn file; and the record is 0600
// in a 0700 directory. Writers of one secret are serialised through the lock
// domain's file locker, whose lock files live beside the records, because a
// Put reads the history to number the new version and two unserialised Puts
// would mint the same number. Reads take no lock: the rename is the
// synchronisation.
//
// With cfg.Key set, nothing readable is written: each record is sealed with
// AES-256-GCM, bound to the secret's name, before it is published — and the
// temporary the publication writes first holds the same sealed bytes.
//
// It refuses at CONSTRUCTION: a configuration it cannot honour
// ([InvalidConfig]), a directory another account can read ([InvalidConfig],
// never narrowed silently), and — with core/proc.UnsupportedPlatform — a
// platform where vfs cannot publish atomically or the lock cannot exclude,
// which today is every platform but the Unix family. The directory is not
// touched when the platform is refused.
//
// The store also implements io.Closer, which releases the directory handle;
// every call after Close fails with core/secret.StoreUnavailable.
func NewFile(cfg FileConfig) (store coresecret.Store, err error) {
	//: the configuration is refused before anything touches the disk.
	if invalid := cfg.validate(); invalid != nil {
		//: InvalidConfig.
		return nil, invalid
	}
	//: ADR 0018: no native mechanic means a typed refusal, decided before a
	//: directory is created that the store could then never use.
	if !platformNative {
		//: the SDK-wide sentinel for exactly this situation.
		return nil, coreproc.UnsupportedPlatform
	}
	//: created 0700 if absent, refused if another account can read it.
	if dirErr := prepareDir(cfg.Dir); dirErr != nil {
		//: InvalidConfig.
		return nil, dirErr
	}
	root, rootErr := svcvfs.NewOS(cfg.Dir)
	//: the directory was just checked, so this is a race or a mount going away.
	if rootErr != nil {
		//: InvalidConfig, with vfs's verdict as a field: it names no path.
		return nil, wrapAs(InvalidConfig, rootErr, errs.String("setting", "Dir"), errs.String("problem", "cannot be opened"))
	}
	clk := cfg.clockOrSystem()
	locker, lockErr := svclock.NewFileLocker(svclock.FileConfig{Dir: cfg.Dir, Clock: clk})
	//: the lock domain refused the directory the store was about to use.
	if lockErr != nil {
		//: close what was opened; its own failure joins the verdict.
		return nil, errors.Join(wrapAs(InvalidConfig, lockErr, errs.String("setting", "Dir"),
			errs.String("problem", "the lock domain refused it")), closeRoot(root))
	}
	//: a working store.
	return &fileStore{
		root:   root,
		closer: rootCloser(root),
		locker: locker,
		key:    cfg.Key,
		sealed: len(cfg.Key.Bytes()) == corecrypto.KeyLen,
		clk:    clk,
	}, nil
}

// Get returns the newest version of name.
func (s *fileStore) Get(ctx context.Context, name string) (current coresecret.VersionValue, err error) {
	versions, readErr := s.Versions(ctx, name)
	//: NotFound, InvalidName, StoreUnavailable or RecordUnreadable.
	if readErr != nil {
		//: the same verdict Versions reached.
		return coresecret.VersionValue{}, readErr
	}
	//: a record always holds at least one version, newest first.
	return versions[0], nil
}

// Versions returns every kept version of name, newest first. It takes no lock:
// a record is replaced by one rename, so a read sees one whole history.
//
// The context is honoured only before the read begins: reading one small file
// is not a wait worth cancelling halfway.
func (s *fileStore) Versions(ctx context.Context, name string) (versions []coresecret.VersionValue, err error) {
	//: a malformed name never becomes a path.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return nil, nameErr
	}
	//: a caller that has already given up is not served.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the context's own error: the caller supplied it and knows what it means.
		return nil, ctxErr
	}
	//: the whole history, or NotFound.
	return s.readHistory(name)
}

// Put stores value as the next version of name, under name's writer lock.
func (s *fileStore) Put(ctx context.Context, name string, value coresecret.Value) (created coresecret.VersionValue, err error) {
	//: the name and the value, checked as every store checks them.
	if putErr := checkPut(name, value.IsZero()); putErr != nil {
		//: InvalidName or EmptyValue.
		return coresecret.VersionValue{}, putErr
	}
	//: the read-modify-write runs with the writer lock held.
	writeErr := s.withWriter(ctx, name, func() error {
		history, readErr := s.readHistory(name)
		//: a secret with no record yet starts its history here.
		if readErr != nil && !errs.HasCode(readErr, coresecret.CodeNotFound) {
			//: StoreUnavailable or RecordUnreadable: nothing is written over
			//: a record this store cannot read.
			return readErr
		}
		created = coresecret.VersionValue{
			Name: name, Version: nextVersion(history), Value: value, Created: s.clk.Now(),
		}
		//: the new version at the head, newest first.
		return s.publish(name, append([]coresecret.VersionValue{created}, history...))
	})
	//: nothing was stored.
	if writeErr != nil {
		//: the lock's, the read's or the publication's verdict.
		return coresecret.VersionValue{}, writeErr
	}
	//: the version as stored.
	return created, nil
}

// Prune keeps only the newest keep versions of name, under name's writer lock.
func (s *fileStore) Prune(ctx context.Context, name string, keep int) error {
	//: the name and the bound, checked as every store checks them.
	if pruneErr := checkPrune(name, keep); pruneErr != nil {
		//: InvalidName or InvalidKeep.
		return pruneErr
	}
	//: the read-modify-write runs with the writer lock held.
	return s.withWriter(ctx, name, func() error {
		history, readErr := s.readHistory(name)
		//: NotFound, StoreUnavailable or RecordUnreadable.
		if readErr != nil {
			//: nothing to prune, or nothing readable.
			return readErr
		}
		//: already within the bound: no write, so no needless publication.
		if len(history) <= keep {
			//: nothing to drop.
			return nil
		}
		//: the newest keep versions, republished as the whole record.
		return s.publish(name, pruned(history, keep))
	})
}

// Names lists every secret with a record, sorted. The lock files and a
// publication's temporary share the directory and are not records.
func (s *fileStore) Names(ctx context.Context) (names []string, err error) {
	//: a caller that has already given up is not served.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the context's own error.
		return nil, ctxErr
	}
	entries, readErr := fs.ReadDir(s.root, ".")
	//: the directory itself could not be listed.
	if readErr != nil {
		//: StoreUnavailable, with vfs's verdict: it names no path.
		return nil, wrapAs(coresecret.StoreUnavailable, readErr, errs.String("operation", "list"))
	}
	names = make([]string, 0, len(entries))
	//: a record is a regular file named "<valid name>.secret".
	for _, entry := range entries {
		name, isRecord := strings.CutSuffix(entry.Name(), recordSuffix)
		//: anything else in the directory is not a secret.
		if isRecord && entry.Type().IsRegular() && coresecret.ValidateName(name) == nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	//: sorted, so two calls answer identically.
	return names, nil
}

// Close releases the directory handle. Every call after it fails.
func (s *fileStore) Close() error {
	//: vfs reports its own verdict; a second Close is its business.
	return s.closer.Close()
}

// readHistory reads and decodes name's record: NotFound when there is none.
func (s *fileStore) readHistory(name string) (versions []coresecret.VersionValue, err error) {
	data, readErr := fs.ReadFile(s.root, recordPath(name))
	//: no record is no secret, which is the one read failure that is an answer.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: NotFound.
		return nil, notFound(name)
	}
	//: anything else is the store failing to answer.
	if readErr != nil {
		//: StoreUnavailable, with vfs's verdict: it names no path.
		return nil, wrapAs(coresecret.StoreUnavailable, readErr, errs.String("secret", name), errs.String("operation", "read"))
	}
	//: the raw record holds the secrets (sealed or not); it is cleared once
	//: decoded, since every Value handed out is its own copy.
	defer clear(data)
	//: the history, or RecordUnreadable.
	return decodeRecord(name, data, s.key, s.sealed)
}

// publish writes name's whole newest-first history as one atomic replacement.
func (s *fileStore) publish(name string, history []coresecret.VersionValue) error {
	encoded, encodeErr := encodeRecord(name, history, s.key, s.sealed)
	//: nothing is published from a document that could not be built.
	if encodeErr != nil {
		//: StoreUnavailable.
		return encodeErr
	}
	//: the bytes are the secrets, sealed or not; they are cleared once written.
	defer clear(encoded)
	//: a temporary beside the record, flushed, renamed over it, the directory
	//: flushed: the previous record is intact on any failure.
	if writeErr := s.root.WriteAtomic(recordPath(name), encoded, recordMode); writeErr != nil {
		//: StoreUnavailable, with vfs's verdict: it names no path.
		return wrapAs(coresecret.StoreUnavailable, writeErr, errs.String("secret", name), errs.String("operation", "publish"))
	}
	//: published.
	return nil
}

// withWriter runs work with name's writer lock held, releasing it whatever
// work returns. A cancelled context while waiting returns the context's own
// error; any other lock failure is StoreUnavailable.
func (s *fileStore) withWriter(ctx context.Context, name string, work func() error) error {
	lease, acquireErr := s.locker.Acquire(ctx, lockPrefix+name)
	//: the caller gave up waiting, or the lock could not be taken at all.
	if acquireErr != nil {
		//: the caller's own deadline is reported as the caller's.
		if ctxErr := ctx.Err(); ctxErr != nil {
			//: context.Canceled or context.DeadlineExceeded.
			return ctxErr
		}
		//: StoreUnavailable, with the lock domain's verdict.
		return wrapAs(coresecret.StoreUnavailable, acquireErr, errs.String("secret", name), errs.String("operation", "lock"))
	}
	workErr := work()
	//: released on a context the caller's cancellation cannot reach, so an
	//: abandoned request does not strand the lock.
	releaseErr := lease.Release(context.WithoutCancel(ctx))
	//: a release failure is reported, but never hides the work's own verdict.
	if releaseErr != nil {
		//: both, the work's first.
		return errors.Join(workErr, wrapAs(coresecret.StoreUnavailable, releaseErr,
			errs.String("secret", name), errs.String("operation", "unlock")))
	}
	//: the work's own verdict.
	return workErr
}

// rootCloser returns the io.Closer vfs.NewOS documents its filesystem to be,
// or a closer that does nothing for a filesystem that holds nothing.
func rootCloser(root corevfs.FullFS) io.Closer {
	//: vfs.NewOS returns a filesystem that implements io.Closer.
	if closer, ok := root.(io.Closer); ok {
		//: the descriptor's owner.
		return closer
	}
	//: nothing held, nothing to release.
	return nopCloser{}
}

// closeRoot releases a root on a construction path that is abandoning it.
func closeRoot(root corevfs.FullFS) error {
	//: the same closer the store would have used.
	return rootCloser(root).Close()
}

// nopCloser closes nothing, for a root that holds no descriptor.
type nopCloser struct{}

// Close does nothing and succeeds.
func (nopCloser) Close() error {
	//: nothing held.
	return nil
}
