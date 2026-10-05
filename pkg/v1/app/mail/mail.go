package mail

import (
	svcmail "github.com/kitsunium/sdk/internal/service/app/mail"
)

// Compose renders msg to the RFC 5322 wire form using the system clock and
// crypto/rand. It is the one-line form of [NewComposer].
func Compose(msg Message) (raw []byte, err error) {
	//: a fresh composer per call: it holds no state between messages.
	return svcmail.NewComposer(ComposerConfig{}).Compose(msg)
}
