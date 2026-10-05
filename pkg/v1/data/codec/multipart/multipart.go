package multipart

import (
// The implementation registers itself with the codec registry as it is
// initialised; importing it registers the format, and the names below
// alias the types it owns.
)

// Format is the name multipart/form-data is registered under. It is an untyped
// constant, so it goes wherever a format name is taken — the codec package's
// Marshal, config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const Format = "multipart"
