// Package asn1 wraps encoding/asn1 as a codec.Codec implementation.
// Only DER (Distinguished Encoding Rules) is emitted because the stdlib
// produces DER exclusively.
//
// Unmarshal reads what encoding/asn1 reads, which is DER with a few BER
// relaxations and no more. Accepted: a UTCTime without seconds or with a
// +hhmm offset instead of Z, a field holding its DEFAULT value written out, a
// SET OF in any order, a PrintableString carrying '*' or '&'. Refused with
// UNMARSHAL_FAILED: an indefinite length, a length or a tag number not in its
// shortest form, a non-minimal INTEGER, a BOOLEAN other than 0x00 or 0xFF, a
// constructed (segmented) string. The registered codec ignores bytes past
// the first decoded structure, as encoding/asn1 returns them; NewStrict's
// instance refuses them.
package asn1
