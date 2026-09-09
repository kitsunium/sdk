// Package token — range 0.3.44.* (ADR 0042 service/token block).
package token

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.44.0 - 0.3.44.255
//
// The domain verdicts a caller matches on live in core/token (0.2.13.*). This
// block holds only what is specific to the two concrete formats implemented
// here — header parameters, key-set selection, PASETO framing.

// CodeHeaderUnsupported identifies a JOSE header this package will not act on:
// a non-empty "crit" (RFC 7515 §4.1.11 makes an unrecognised critical
// parameter a mandatory rejection), or a "typ" that does not match the one the
// verifier requires.
const CodeHeaderUnsupported errs.Code = 0x00_03_2C_01 // 0.3.44.1

// CodeKeyNotFound identifies a token whose "kid" names no key in the JWK Set
// the verifier was built from.
const CodeKeyNotFound errs.Code = 0x00_03_2C_02 // 0.3.44.2

// CodeKeyIDMissing identifies a token presented to a key-set verifier with no
// "kid" header. A set verifier selects by id; it does not try every key it
// holds.
const CodeKeyIDMissing errs.Code = 0x00_03_2C_03 // 0.3.44.3

// CodeKeyIDAmbiguous identifies a "kid" carried by more candidates than the
// verifier will try. Trying a bounded number of candidates is the rotation
// path; trying an unbounded number is a CPU amplifier a publisher controls.
const CodeKeyIDAmbiguous errs.Code = 0x00_03_2C_04 // 0.3.44.4

// CodeFooterMismatch identifies a PASETO token whose footer is not the one the
// verifier expects — including a footer present where none was configured.
const CodeFooterMismatch errs.Code = 0x00_03_2C_05 // 0.3.44.5

// CodeSchemeUnsupported identifies a PASETO version+purpose this package does
// not implement: any local (encrypted) purpose, and every version other than
// v4. See the package CLAUDE.md for why v4.local is absent.
const CodeSchemeUnsupported errs.Code = 0x00_03_2C_06 // 0.3.44.6

// CodeDuplicateMember identifies a JOSE header or claims object carrying the
// same member name twice. Go's encoding/json silently keeps the last one, so
// two readers of the same bytes could disagree about what the token says
// (RFC 8725 §2.6).
const CodeDuplicateMember errs.Code = 0x00_03_2C_07 // 0.3.44.7
