// Package secret — the read-only store over the process environment, with the
// NAME_FILE convention Docker and Kubernetes secrets use.
//
// Package secret — the file store: a directory the store owns, one record per
// secret published atomically through vfs, writers serialised across
// processes through the lock domain, and optional sealing at rest.
//
// Package secret — the file store's construction parameters and the checks
// that refuse a directory the store cannot keep secrets in.
//
// Package secret — the file store's on-disk record: one JSON document per
// secret, sealed when the store has a key.
//
// Package secret — the file store's honest refusal where its mechanics do not
// exist (ADR 0018 §(a): a uniform typed sentinel, never a silent downgrade and
// never a build break).
//
// The store rests on vfs.NewOS, which refuses the same platforms for reasons
// its own osguard_other.go records: on Windows a directory cannot be flushed,
// so a published name and the bytes it names can disagree after a crash, and
// a permission mode is not an ACL, so 0600 would exclude nobody. A secret
// store that reported success while doing neither is the one this domain
// exists not to ship. The memory and environment stores work everywhere.
//
// Package secret — the file store's platform gate, on the platforms that have
// what it rests on: vfs's atomic publication with a flushed directory entry,
// permission bits that exclude other accounts, and the lock domain's flock(2).
//
// Package secret — the machine-local key file: one crypto.Key, created on
// first use, the same key for every process that asks at once.
//
// Package secret — the keyring: the versions of one secret seen as keys.
//
// Package secret — a keyring read once: the root view a pass over many
// wrapped keys uses.
//
// Package secret — the in-process store.
//
// Package secret — the rotator: a policy that mints new versions of one
// secret on a schedule, keeping enough old ones that nothing sealed before a
// rotation breaks because of it.
//
// Package secret — the subject box: what SubjectKeys.Seal returns, and the
// associated data it and a wrapped key are bound to.
//
// Package secret — the bounded cache of opened subject keys, and the order it
// keeps between filling an entry and destroying a key.
//
// Package secret — subject keys: one data key per subject, wrapped by a root
// keyring that rotates, and destroyed to erase everything it sealed (ADR 0142).
//
// Package secret — the in-process subject-key store.
//
// Package secret — what a rotation of the root costs the subject keys: one
// re-wrap per subject, and a version kept while a key still needs it.
//
// Package secret provides the concrete secret stores — in memory, the process
// environment, a directory on disk — the Keyring that sees one secret's
// versions as keys, and the Rotator that mints new versions on a schedule
// (ADR 0096). Every store implements core/security/secret.Store.
//
// This file holds the argument checks every store applies identically and the
// version arithmetic they share, so that three backends cannot disagree about
// what a valid Put or a correct prune is.
//
// Package secret — the two ways this package turns a failure into a verdict:
// storeFailure, around the error a caller's SubjectKeyStore returned, and
// wrapAs, around one of the domain's sentinels. Every sentinel and code it
// raises is declared in internal/core/security/secret (ADR 0160); this
// package declares none.
//
// No Public, Private or field built here carries a secret, a data key or a
// sealed box. A secret's name, a version number, an environment variable's
// NAME, a valid subject reference and an operation may travel as log-only
// fields; a file path does not, because the directory a store owns is a
// deployment detail an error has no reason to repeat.
package secret
