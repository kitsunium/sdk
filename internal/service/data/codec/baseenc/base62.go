// Package baseenc — Base62 (0-9A-Za-z) base-conversion codec.
package baseenc

// base62Alphabet is the conventional Base62 alphabet: digits, then uppercase,
// then lowercase ASCII letters.
const base62Alphabet string = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// base62Radix is the Base62 alphabet size.
const base62Radix int = 62

// base62Reverse maps an ASCII byte to its Base62 index, or base45NotMember
// when the byte is absent from the alphabet.
var base62Reverse = buildReverseTable(base62Alphabet)
