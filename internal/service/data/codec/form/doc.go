// Package form implements application/x-www-form-urlencoded as a
// codec.Codec — the encoding every HTML form POSTs and every query string
// carries. It is stdlib-only: net/url supplies the decode half.
//
// The wire format is a flat, ordered list of key=value pairs joined by '&',
// with keys and values percent-escaped and space written as '+'. It has no
// types, no nesting, and no array syntax: the ONLY way to carry more than one
// value under a key is to repeat the key. This codec models that literally,
// so its native Go shape is url.Values (== map[string][]string). Marshal
// accepts url.Values, map[string][]string and — for the common single-valued
// form — map[string]string, plus non-nil pointers to any of the three;
// Unmarshal writes into a pointer to any of the three.
//
// Repeated keys: "a=1&a=2" decodes to {"a": ["1","2"]}, in wire order. No
// bracket dialect (a[]=, a[0]=) is invented and no value is dropped — see the
// package CLAUDE.md for the full round-trip contract and the two documented
// places where Marshal ∘ Unmarshal is not byte-for-byte identity.
package form
