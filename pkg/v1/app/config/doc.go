// Package config is the public facade for the SDK's configuration domain: layer
// configuration from env + files, [Load] into a typed struct (later sources
// override earlier), self-[Validator] the result, and [PollWatcher] for cross-OS
// hot-reload.
//
//	type Conf struct{ Port int `json:"port"` }
//	var c Conf
//	err := config.Load(&c,
//	    config.FileSource("json", "/etc/app.json"), // blank-import pkg/v1/data/codec
//	    config.EnvSource("APP"),                     // APP_PORT overrides the file
//	)
//
// File parsing dispatches through the codec registry — blank-import the format's
// codec (e.g. pkg/v1/data/codec) so it is registered. Failures surface typed
// sentinels (SourceFailed / DecodeFailed / ValidationFailed / WatchFailed).
//
// # A configuration carried in the binary
//
// [FSSource] is [FileSource] over an io/fs.FS, for a program whose committed
// configuration travels inside it:
//
//	//go:embed config
//	var files embed.FS
//
//	err := config.Load(&c,
//	    config.FSSource(files, "yaml", "config/config.yaml"),
//	    config.EnvSource("APP"), // the environment still wins
//	)
//
// It dispatches through the same codecs, refuses with the same SourceFailed,
// and describes itself the same way — a traced load reports its keys under
// the layer "file" with the path as given. A file the filesystem does not hold
// is refused, as FileSource refuses one it cannot open, and never read as an
// empty layer: absent from an embedded tree usually means an embed pattern
// that matched nothing, and read as empty it would start the program on its
// defaults without a word. A layer that is optional by design — one document
// per environment, where some environments have none — is the caller's
// decision, one fs.Stat away, and only an absence makes it optional:
//
//	name := "config/" + env + ".yaml"
//	switch _, err := fs.Stat(files, name); {
//	case err == nil:
//	    sources = append(sources, config.FSSource(files, "yaml", name))
//	case !errors.Is(err, fs.ErrNotExist):
//	    return err // present but unreadable is not the optional absence
//	}
//
// # The schema: required keys, defaults, and a closed vocabulary
//
// [NewSchema] compiles a [SchemaSpec] into a [Schema]: which keys the
// application cannot start without, the typed [Default] each key takes when
// nobody supplies it, and the constraints the decoded result must satisfy — the
// `validate` struct tags of the type, composed with any cross-field rule the
// caller adds. [LoadSchema] is [Load] with one.
//
//	type Conf struct {
//	    Port     int    `json:"port"      validate:"min=1,max=65535"`
//	    Database struct {
//	        DSN      string `json:"dsn"       validate:"required"`
//	        MaxConns int    `json:"max_conns" validate:"min=1,max=512"`
//	    } `json:"database" validate:"dive"`
//	}
//
//	schema, err := config.NewSchema[Conf](config.SchemaSpec[Conf]{
//	    Required: []string{"database.dsn"},
//	    Defaults: []config.Default{
//	        {Key: "port", Value: 8080},
//	        {Key: "database.max_conns", Value: 16},
//	    },
//	})
//	// …
//	var c Conf
//	err = config.LoadSchema(&c, schema,
//	    config.FileSource("json", "/etc/app.json"),
//	    config.EnvSource("APP"),
//	)
//
// Five properties are the reason it exists.
//
// A required key that no source supplied fails the LOAD — at start-up, before
// anything reads the value, which is the entire point of declaring one. Every
// missing key is named in ONE error rather than the first one found, so an
// operator does not restart the service once per typo. It is a key-level check,
// decided on the merged map while the key is still a key; `validate:"required"`
// is the value-level one, decided after the decode. `port = 0` satisfies the
// first and fails the second. Declare both when both are meant.
//
// A key no field of the type addresses is REFUSED by default. Ignoring it is
// the classic production incident: APP_PORTT=9090 decodes into nothing, the
// process starts on the old port, and the only evidence is the absence of an
// effect. Set [SchemaSpec].AllowUnknownKeys where the source genuinely carries
// more than this type reads — a file shared by two services, or an unprefixed
// EnvSource, which hands over every variable in the environment.
//
// A default is a LAYER, not a post-decode fallback. It is merged under every
// source before the decode, so presence is decided while the operator's key is
// still a key: an omitted "port" takes 8080, and `port = 0` written in a file
// stays 0. The layer order is default < file < env < any later source, and the
// default layer is placed first structurally — there is no way to spell a load
// in which it wins, because a default that could win is not a default. A key
// may not be both required and defaulted: the schema would fill it itself, so
// the requirement could never fire.
//
// A violation names the OPERATOR's key ("database.max_conns"), never the Go
// field, because every format is decoded through a json round trip and the json
// tag is literally the key they typed.
//
// A message never contains the value that was refused. A configuration value is
// routinely a password, a token or a connection string, and a validation
// message is the one error message designed to reach a human. The error carries
// the violation count, the first rule and the offending keys; [Schema].Check
// returns the full report when per-key messages are wanted.
//
// A schema that contradicts itself — a default outside the bounds it also
// declares, a key naming no field of the type, the same key twice, a key both
// required and defaulted — is refused by [NewSchema] with SchemaInvalid, before
// any source is read. A schema that declares nothing is legitimate and accepts
// (and still refuses an unknown key).
//
// [SchemaSpec].Rule and [Schema].Check speak in the vocabulary of
// pkg/v1/app/validation: a rule is a validation.Constraint and a report is a
// validation.Report. They are the same types, not converted ones — this domain
// composes that engine rather than reimplementing it, and a rule written for an
// HTTP body is the same value here. The schema describes the SHAPE (which keys,
// required or not, and what they default to); the constraints on a VALUE stay
// where they already are.
//
// # Where each value came from
//
// [LoadWithOrigins] and [LoadSchemaWithOrigins] are the same loads, and they
// also return one [Origin] per leaf key of the target, sorted: the layer that
// supplied its final value — "default", "file", "env", or a kind a source
// names through [Describer] — and the detail an operator acts on, such as the
// variable "APP_DATA_DIR" or the file "/etc/app.json". A key absent from the
// merged layers has an empty Layer — no layer supplied it, or a later layer
// erased it by replacing one of its tables with a scalar or a null.
//
//	origins, err := config.LoadSchemaWithOrigins(&c, schema,
//	    config.FileSource("json", "/etc/app.json"),
//	    config.EnvSource("APP"),
//	)
//	for _, o := range origins {
//	    fmt.Printf("%-20s %-8s %s\n", o.Key, o.Layer, o.Detail) // never the value
//	}
//
// An origin never carries a value. A key whose field holds a secret.Value
// (pkg/v1/security/secret) is marked Secret, so a --show-config rendering knows to mask
// what it prints beside it — and such a field is filled from the environment's
// RAW text: the JSON coercion that turns "8080" into an int would turn a
// numeric secret like "1e3" into 1000, so it is bypassed for secrets, on every
// entry point, traced or not. No refusal in this package ever repeats a value,
// a secret's least of all.
package config
