// Package secret — ranges 0.2.37.* (the port's verdicts) and 0.3.68.* (the
// engines' own refusals) — ADR 0096 and ADR 0142, declared here since ADR 0160.
//
// Package secret — declares the sentinel *errs.Error outcomes: the verdicts of
// the ports, and the refusals of the concrete stores, the keyring, the rotator
// and the subject keys in internal/service/security/secret (ADR 0160). Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private here carries a secret, and no field ever will: the
// whole domain exists so that a value can travel through a program without
// being written down by accident, and an error message is the most-written
// text a program produces. A secret's NAME is not secret and may travel as a
// log-only field, because an operator cannot act on "a secret is missing"
// without knowing which one.
//
// Package secret — the secret-name grammar, the one every store shares.
//
// Package secret declares the secret domain (ADR 0096): the [Value] a secret
// travels in, the [Store] port that keeps named secrets as numbered versions,
// the [VersionValue] a store hands back, and the one name grammar every store
// shares. Concrete stores — in memory, the process environment, a directory on
// disk — the keyring that sees a secret's versions as keys, and the rotator
// that mints them live in internal/service/security/secret; this package owns only the
// contract.
//
// # What the domain is for
//
// A secret has three properties no other value in a program has, and each of
// them is a mechanism here rather than a convention a caller must remember:
//
//   - it must not be WRITTEN DOWN by accident. [Value] renders as a fixed
//     placeholder in every rendering there is, so a struct printed while
//     debugging, a configuration dumped as JSON, and an attribute a logger
//     formats all print the placeholder, and only an explicit Reveal gets the
//     bytes out;
//   - it CHANGES. A key that is never rotated is a key whose compromise is
//     permanent, so a store keeps numbered versions instead of one value, and
//     the newest is current while the older ones stay readable until pruned;
//   - it comes from SOMEWHERE ELSE — an orchestrator, a mounted volume, a
//     directory the process owns. The [Store] port is the seam that lets the
//     same code read any of them, and the one a Vault, KMS or cloud secret
//     manager plugs into later without the code that reads secrets changing.
//
// # There is no registry
//
// Like lock (ADR 0052) and session (ADR 0045), this domain has no
// name->implementation registry. Which store holds a deployment's secrets is a
// decision made in code where the program is wired: resolving it from a
// configuration string would let a typo turn an encrypted store into an
// environment lookup, and the first symptom would be a missing secret in
// production.
//
// Package secret — the subject grammar, and the port that keeps the wrapped
// data key of each subject (ADR 0142).
//
// Package secret — the Value: a secret that every rendering writes as a
// placeholder, and that only an explicit Reveal hands back.
package secret
