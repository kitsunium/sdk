//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/codec/json .

// Package json registers the JSON codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/codec/json"
//
// Everything that dispatches through the registry by format name then reads
// and writes JSON: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/codec instead registers every format the SDK
// ships — BSON with the MongoDB driver, CBOR, MessagePack and the rest — which
// a program that only reads JSON does not need to link. This package links
// the JSON codec and the standard library's encoding/json, and nothing else.
//
// Importing both packages is harmless: a format is registered by the package
// that implements it, which Go initialises once however many packages import
// it.
package json

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is the whole of this package's job.
	_ "github.com/kitsunium/sdk/internal/service/codec/json"
)

// Format is the name JSON is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "json"
