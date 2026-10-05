//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/security/secret .

// Package secret is the public facade for the SDK's secret domain (ADR 0096):
// a [Value] that no rendering writes down, a [Store] that keeps named secrets
// as numbered versions, a [Keyring] that turns those versions into keys, and a
// [Rotator] that mints new ones on a schedule.
//
//	type Config struct {
//	    Port     int          `json:"port"`
//	    Database secret.Value `json:"database_url"` // decodes like a string
//	}
//	fmt.Printf("%+v\n", cfg)              // {Port:8080 Database:<redacted>}
//	db, err := sql.Open("pgx", cfg.Database.RevealString())
//
// # A Value renders as a placeholder, everywhere
//
// fmt with every verb (String, GoString and Format are all implemented, so
// %v, %+v, %#v, %s and %q agree), encoding/json (MarshalJSON writes the
// placeholder string, so a configuration dumped for a --show-config flag does
// not leak), encoding.TextMarshaler (so TOML, YAML and log/slog's text handler
// agree), and log/slog's JSON handler: every one writes [Redacted]. The bytes
// come out only through [Value].Reveal and [Value].RevealString.
//
// A Value decodes from a JSON string or from text, so config.Load
// (pkg/v1/app/config) fills a Value field from a file or the environment exactly as it fills a string —
// and it refuses two inputs, with ValueRefused: a JSON token that is not a
// string, because a number has been re-spelled before a decoder sees it (1e3
// arrives as 1000; twenty digits lose their tail to float64), and the
// placeholder itself, because finding it on the way in means a rendered
// configuration was loaded as a real one.
//
// == does not compile on a Value and neither does using one as a map key; use
// [Value].Equal, which compares in constant time.
//
// # Stores
//
// A [Store] holds versions numbered from 1, never reused, newest first:
//
//   - [NewMemory] keeps them in this process — tests, development, a secret
//     regenerated at every start;
//   - [NewEnv] reads the environment, read-only: the secret "smtp-url" under
//     the prefix "APP" is APP_SMTP_URL, or the content of the file
//     APP_SMTP_URL_FILE names — the Docker and Kubernetes convention. Setting
//     both is refused rather than resolved;
//   - [NewFile] keeps them in a directory it owns: 0700, one 0600 record per
//     secret published atomically, writers serialised across processes, and,
//     given a key, nothing readable written at all.
//
// A name is 1 to 63 characters of a-z, 0-9 and '-', starting and ending with
// a letter or a digit ([ValidateName]) — one grammar that survives every
// filesystem and maps one-to-one onto an environment variable.
//
// # A machine-local key for the file store
//
// [KeyFile] returns the key held in a file, creating it on first use:
//
//	key, err := secret.KeyFile("/var/lib/app/store.key") // 32 raw bytes, 0600
//	store, err := secret.NewFile(secret.FileConfig{Dir: "/var/lib/app/secrets", Key: key})
//
// The file holds exactly 32 RAW bytes and nothing else is accepted — not base64,
// not a trailing newline — because a key reshaped to fit is another key. An
// absent file is created from crypto/rand and published with a hard link, so
// processes racing on first use all return the one key that won; a file any
// other account can read is refused, as the store's directory is.
//
// # Keyring and rotation
//
//	store, _ := secret.NewFile(secret.FileConfig{Dir: "/var/lib/app/secrets", Key: kek})
//	rotator, _ := secret.NewRotator(secret.RotatorConfig{
//	    Store: store, Name: "session-key",
//	    Policy: secret.Policy{Every: 30 * 24 * time.Hour, Keep: 3, Generate: secret.Random(32)},
//	})
//	_, _ = rotator.Ensure(ctx)          // creates version 1 the first time
//	keyring, _ := secret.NewKeyring(store, "session-key")
//	box, _ := keyring.Seal(ctx, cookie, nil) // sealed under the newest version
//	go func() { _ = rotator.Run(ctx) }()  // rotates every 30 days
//	plain, _ := keyring.Open(ctx, box, nil)  // still opens after a rotation
//
// The newest version seals and signs; every kept version still opens and
// verifies, because each box and each signature carries the number of the
// version that made it. A version stops opening when it is pruned — and a
// rotation keeps [Policy].Keep versions, at least two, so the one it replaces
// keeps working.
//
// # One key per subject, and an erasure that reaches every copy
//
// [SubjectKeys] keeps one data key per SUBJECT — a person, a tenant, a record:
// whoever the caller files keys under — wrapped by a root [Keyring] that
// rotates, and seals and opens under it (ADR 0142):
//
//	keys, _ := secret.NewSubjectKeys(secret.SubjectKeysConfig{
//	    Root:  root,                    // a Keyring over the root secret, "data-key"
//	    Store: store,                   // your SubjectKeyStore, or NewMemorySubjectKeyStore()
//	    CacheSize: 10_000, CacheTTL: time.Minute,
//	})
//	box, _ := keys.Seal(ctx, ref, []byte(email), "reports", id, "/email")
//	plain, _ := keys.Open(ctx, box, "reports", id, "/email")
//	_, _ = keys.Destroy(ctx, ref)       // every box sealed for ref stops opening
//	_, err := keys.Open(ctx, box, "reports", id, "/email") // KeyDestroyed
//
// A subject is a REFERENCE, never an identity: it is kept in clear in the store
// and in every box ([ValidateSubject]: lowercase, 1 to [MaxSubjectLen] bytes).
// Derive it — an HMAC of the identity, in hexadecimal. The parts after the
// plaintext bind the box to where it lies, so a box copied into another record
// or field does not open; they are length-prefixed, so ("ab", "c") and ("a",
// "bc") differ.
//
// Destroying a subject's key is a cryptographic erase (NIST SP 800-88r2): the
// boxes it sealed stop opening wherever they were copied — former versions, a
// dead letter, a backup of the data — because nothing holds the key. Open then
// answers [KeyDestroyed], which a caller reads as "erased", and never
// [SubjectKeyUnreadable], which is a fault: a key held that does not unwrap.
// The erasure reaches this process's cache at once, another process's within
// its CacheTTL, and a copy of the wrapped key a backup of the KEY store kept
// only while the root version that wrapped it is kept.
//
// A rotation of the root costs one small write per subject and never touches a
// box. Wire the rotator so it re-wraps after each rotation and never prunes a
// version a key still needs:
//
//	rotator, _ := secret.NewRotator(secret.RotatorConfig{
//	    Store: secrets, Name: "data-key",
//	    Policy:   secret.Policy{Every: 30 * 24 * time.Hour, Keep: 3, Generate: secret.Random(32)},
//	    InUse:    keys.OldestRoot,      // Keep becomes a floor while a key lags
//	    OnRotate: func(secret.Versioned) { _, _ = keys.Rewrap(ctx) },
//	})
//
// # What this package does not do
//
// It ships no Vault, KMS or cloud secret-manager client: those are [Store]
// implementations a connector provides, and the port is frozen so one can be
// written today. It does not reload TLS certificates. It does not lock memory
// or promise that a revealed secret is erased: Go's collector moves and copies
// memory, and a string cannot be cleared.
//
// Package secret — subject keys: one data key per subject under a rotating
// root, and the erasure that destroys it (ADR 0142).
package secret
