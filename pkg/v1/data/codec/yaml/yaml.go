package yaml

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/yaml"
)

// Format is the name YAML is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "yaml"
