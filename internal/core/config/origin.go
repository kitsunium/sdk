// Package config — provenance: which layer supplied a key's final value, and
// the sibling a Source implements to say so.
package config

// The layer names a traced load reports. They are strings, not an enum, on
// purpose: a Source that is none of these names its own kind through
// [Describer], and a closed set would force it to lie.
const (
	// LayerDefault is the schema's default layer, merged under every source.
	LayerDefault string = "default"
	// LayerFile is a file source; its detail is the file's path.
	LayerFile string = "file"
	// LayerEnv is the environment source; its detail is the VARIABLE that set
	// the key, such as "APP_DATA_DIR".
	LayerEnv string = "env"
	// LayerSource is a source that does not describe itself; its detail is
	// its position in the sources a load was given, counting from 0.
	LayerSource string = "source"
)

// OriginValue says which layer supplied the final value of one key of a load —
// and says it WITHOUT the value, which is the point: an origin is written to a
// start-up log, a --show-config table, a diagnostics page, and a
// configuration value is routinely a password.
//
// pkg/v1/config aliases it as Origin. It is a published shape the SDK RETURNS,
// so ADR 0040 applies with its full cost after v1.
type OriginValue struct {
	// Key is the operator's key in the dotted grammar — "database.dsn".
	Key string
	// Layer names the kind of layer that supplied the final value: one of the
	// Layer* constants, or the kind a [Describer] reports. It is EMPTY when the
	// key is absent from the merged layers — no layer supplied it, or a later
	// layer erased it by replacing one of its tables with a scalar or a null —
	// and the field then holds its Go zero value.
	Layer string
	// Detail is what an operator acts on: the variable that set the key, the
	// file that holds it. Empty when the layer has nothing more to say.
	Detail string
	// Secret reports that the key's field holds a core/secret.Value. The
	// origin never carries a value either way; Secret is what tells a
	// rendering that the value, if it shows one, must be masked.
	Secret bool
}

// Describer is the optional sibling (ADR 0039) a [Source] implements to say
// where its values come from. [Source] is frozen at one method, so the loader
// discovers this capability by type assertion; a Source that does not
// implement it is reported as [LayerSource] with its position.
//
// Implementations MUST be safe for concurrent use and MUST NOT return a value
// in detail — a variable NAME, a file PATH, an endpoint, never what it holds.
type Describer interface {
	// Describe reports the kind of layer this source contributes and, for one
	// dotted key it supplied in its last Load, the detail an operator acts on.
	Describe(key string) (layer, detail string)
}
