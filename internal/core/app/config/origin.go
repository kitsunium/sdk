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
