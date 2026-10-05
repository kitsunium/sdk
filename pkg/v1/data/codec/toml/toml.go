package toml

import (
// The implementation registers itself with the codec registry as it is
// initialised; importing it is most of this package's job.
)

// Format is the name TOML is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "toml"
