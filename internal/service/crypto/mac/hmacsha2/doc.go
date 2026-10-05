// Package hmacsha2 registers the "hmac-sha256" MAC scheme (ADR 0014).
// Importing the package (typically a blank import via pkg/v1/crypto/mac) self-registers
// the scheme so crypto.MACTag / crypto.MACVerify resolve. It is stdlib-only
// (crypto/hmac + crypto/sha256), so it pulls zero non-stdlib deps and keeps
// pkg/v1/crypto/mac consumers dep-light.
//
// HMAC-SHA256 (RFC 2104) is the modern default for detached keyed
// authentication. A tag IS secret-comparison-sensitive: Verify routes through
// hmac.Equal (constant-time), never a plain ==/bytes.Equal, the exact inverse of
// the unkeyed Hasher rule.
package hmacsha2
