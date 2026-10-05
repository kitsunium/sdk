// Package asn1 wraps encoding/asn1 as a codec.Codec implementation.
// Only DER (Distinguished Encoding Rules) is emitted because the stdlib
// produces DER exclusively; BER parsing is still accepted on Unmarshal.
package asn1
