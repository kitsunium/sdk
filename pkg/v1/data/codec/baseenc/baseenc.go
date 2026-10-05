package baseenc

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/baseenc"
)

// The nine Formats this package registers. Each is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const (
	// Base64 is standard base64, RFC 4648 §4.
	Base64 = "base64"
	// Base64URL is the URL- and filename-safe base64 alphabet, RFC 4648 §5.
	Base64URL = "base64url"
	// Base32 is standard base32, RFC 4648 §6.
	Base32 = "base32"
	// Base16 is upper-case hexadecimal, RFC 4648 §8.
	Base16 = "base16"
	// Hex is lower-case hexadecimal.
	Hex = "hex"
	// ASCII85 is Adobe's Ascii85, four bytes in five characters.
	ASCII85 = "ascii85"
	// Base45 is RFC 9285's alphabet, the one a QR code's alphanumeric mode holds.
	Base45 = "base45"
	// Base58 is Bitcoin's alphabet; its base conversion is quadratic, so its input is capped at 4 KiB.
	Base58 = "base58"
	// Base62 is the alphanumeric alphabet; capped at 4 KiB like Base58.
	Base62 = "base62"
)
