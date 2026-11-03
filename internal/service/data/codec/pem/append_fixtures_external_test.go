package pem_test

import (
	"bytes"

	"github.com/kitsunium/sdk/internal/service/data/codec/pem"
)

// appendFixtures are the blocks whose Append must write Marshal's bytes:
// with and without headers (Proc-Type first, the rest sorted), an empty body,
// a body ending on a line boundary, and the promotion shape.
func appendFixtures() map[string]any {
	return map[string]any{
		"plain":           &pem.Block{Type: "TEST", Bytes: []byte("hello world")},
		"empty body":      &pem.Block{Type: "EMPTY"},
		"line boundary":   &pem.Block{Type: "B", Bytes: bytes.Repeat([]byte{0xa5}, 96)},
		"headers":         &pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00", "Comment": "x"}, Bytes: []byte{1, 2, 3}},
		"headers no proc": &pem.Block{Type: "H", Headers: map[string]string{"b": "2", "a": "1"}, Bytes: []byte("z")},
		"promotion shape": &pem.Block{Type: "JSON", Bytes: []byte(`{"a":"b"}`)},
		"one kibibyte":    kibValue(),
	}
}

// appendFailure is a block encoding/pem refuses: a header key with a colon.
func appendFailure() any {
	//: refused before a byte is written.
	return &pem.Block{Type: "BAD", Headers: map[string]string{"a:b": "c"}, Bytes: []byte("x")}
}

// kibValue is a block of 1 KiB.
func kibValue() *pem.Block {
	return &pem.Block{Type: "CERTIFICATE", Bytes: bytes.Repeat([]byte("0123456789abcdef"), 64)}
}
