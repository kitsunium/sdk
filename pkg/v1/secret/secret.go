//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/secret .

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
// come out only through [Value.Reveal] and [Value.RevealString].
//
// A Value decodes from a JSON string or from text, so config.Load
// (pkg/v1/config) fills a Value field from a file or the environment exactly as it fills a string —
// and it refuses two inputs, with ValueRefused: a JSON token that is not a
// string, because a number has been re-spelled before a decoder sees it (1e3
// arrives as 1000; twenty digits lose their tail to float64), and the
// placeholder itself, because finding it on the way in means a rendered
// configuration was loaded as a real one.
//
// == does not compile on a Value and neither does using one as a map key; use
// [Value.Equal], which compares in constant time.
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
// # What this package does not do
//
// It ships no Vault, KMS or cloud secret-manager client: those are [Store]
// implementations a connector provides, and the port is frozen so one can be
// written today. It does not reload TLS certificates. It does not lock memory
// or promise that a revealed secret is erased: Go's collector moves and copies
// memory, and a string cannot be cleared.
package secret

import (
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
	"github.com/kitsunium/sdk/pkg/v1/crypto"
)

// Redacted is the placeholder every rendering of a [Value] writes.
const Redacted string = coresecret.Redacted

// MaxNameLen is the longest secret name, in bytes.
const MaxNameLen int = coresecret.MaxNameLen

// Value is an immutable secret that renders as [Redacted] in every rendering;
// only Reveal and RevealString hand its bytes back.
type Value = coresecret.Value

// Store keeps named secrets as numbered versions. It is frozen at five
// methods; a new capability arrives as a sibling interface.
type Store = coresecret.Store

// Versioned is one version of one secret: name, number, value, and when the
// store recorded it.
type Versioned = coresecret.VersionValue

// MemoryConfig parameterises [NewMemory].
type MemoryConfig = svcsecret.MemoryConfig

// EnvConfig parameterises [NewEnv].
type EnvConfig = svcsecret.EnvConfig

// FileConfig parameterises [NewFile].
type FileConfig = svcsecret.FileConfig

// Keyring sees the versions of one secret as keys: the newest seals and signs,
// every kept version opens and verifies.
type Keyring = svcsecret.Keyring

// Policy says how often a secret rotates, how many versions a rotation keeps,
// and how a new one is made.
type Policy = svcsecret.PolicySpec

// RotatorConfig parameterises [NewRotator].
type RotatorConfig = svcsecret.RotatorConfig

// Rotator mints new versions of one secret according to a [Policy].
type Rotator = svcsecret.Rotator

var (
	// NotFound is returned for a name that holds no version.
	NotFound = coresecret.NotFound
	// InvalidName is returned for a name outside the grammar.
	InvalidName = coresecret.InvalidName
	// ReadOnly is returned by a write to a store that only reads.
	ReadOnly = coresecret.ReadOnly
	// StoreUnavailable is returned when the backend could not be read or
	// written; a retry is meaningful.
	StoreUnavailable = coresecret.StoreUnavailable
	// ValueRefused is returned by a decode given anything but a string, or
	// the placeholder itself.
	ValueRefused = coresecret.ValueRefused
	// EmptyValue is returned by a Put of an empty secret.
	EmptyValue = coresecret.EmptyValue
	// InvalidKeep is returned by a Prune asked to keep fewer than one version.
	InvalidKeep = coresecret.InvalidKeep
	// InvalidConfig is returned by a constructor given a configuration it
	// cannot honour.
	InvalidConfig = svcsecret.InvalidConfig
	// RecordUnreadable is returned by the file store for a record it found
	// and could not read back — most often, one written under another key.
	RecordUnreadable = svcsecret.RecordUnreadable
	// EnvRefused is returned when the environment names a secret without a
	// usable value: both forms set, or an empty or oversized file.
	EnvRefused = svcsecret.EnvRefused
	// SealInvalid is returned by Keyring.Open for every box it cannot open.
	SealInvalid = svcsecret.SealInvalid
	// SignatureInvalid is returned by Keyring.Verify for every signature it
	// cannot verify.
	SignatureInvalid = svcsecret.SignatureInvalid
	// KeyMaterialInvalid is returned when a keyring version is not exactly 32
	// bytes.
	KeyMaterialInvalid = svcsecret.KeyMaterialInvalid
	// GenerateFailed is returned when a rotation's generator failed.
	GenerateFailed = svcsecret.GenerateFailed
	// KeyFileInvalid is returned by KeyFile for a file that exists and does not
	// hold exactly one key of raw bytes.
	KeyFileInvalid = svcsecret.KeyFileInvalid
)

// New returns a Value holding a copy of raw.
func New(raw []byte) Value {
	//: delegate to the core constructor.
	return coresecret.NewValue(raw)
}

// FromString returns a Value holding text.
func FromString(text string) Value {
	//: delegate to the core constructor.
	return coresecret.FromString(text)
}

// ValidateName reports whether name is a secret name, returning [InvalidName]
// when it is not. Every store calls it; a caller may too, to refuse a name at
// the edge of its own input.
func ValidateName(name string) error {
	//: delegate to the core grammar.
	return coresecret.ValidateName(name)
}

// NewMemory returns a Store that keeps its versions in this process's memory.
func NewMemory(cfg MemoryConfig) Store {
	//: delegate to the service constructor.
	return svcsecret.NewMemory(cfg)
}

// NewEnv returns a read-only Store over the process environment, honouring the
// NAME_FILE convention. It refuses a prefix no shell could export.
func NewEnv(cfg EnvConfig) (store Store, err error) {
	//: delegate to the service constructor.
	return svcsecret.NewEnv(cfg)
}

// NewFile returns a Store that keeps its versions in a directory it owns,
// sealed at rest when cfg.Key is set. It refuses a directory other accounts
// can read, and returns proc.UnsupportedPlatform where atomic publication or
// the file lock is unavailable. The store also implements io.Closer.
func NewFile(cfg FileConfig) (store Store, err error) {
	//: delegate to the service constructor.
	return svcsecret.NewFile(cfg)
}

// NewKeyring returns the keyring over the versions of name in store.
func NewKeyring(store Store, name string) (keyring *Keyring, err error) {
	//: delegate to the service constructor.
	return svcsecret.NewKeyring(store, name)
}

// NewRotator returns a rotator for cfg.Name in cfg.Store. It starts nothing.
func NewRotator(cfg RotatorConfig) (rotator *Rotator, err error) {
	//: delegate to the service constructor.
	return svcsecret.NewRotator(cfg)
}

// KeyFile returns the crypto.Key held in the file at path — exactly
// crypto.KeyLen raw bytes — creating the file with a fresh random key on first
// use: 0600, its directory 0700 when it has to be made, published atomically so
// that concurrent first uses all return the same key.
//
// It refuses content that is not exactly one key with [KeyFileInvalid], an
// existing file other accounts can read with [InvalidConfig], and — where the
// file store refuses, every platform outside the Unix family, because a mode
// is not an access list there — proc.UnsupportedPlatform. No error carries the
// key or a refused file's content.
func KeyFile(path string) (key crypto.Key, err error) {
	//: delegate to the service implementation.
	return svcsecret.KeyFile(path)
}

// Random returns a generator of n bytes from crypto/rand; a keyring's versions
// need n = 32. Below 16 bytes the generator refuses every call.
func Random(n int) func() (Value, error) {
	//: delegate to the service generator.
	return svcsecret.Random(n)
}
