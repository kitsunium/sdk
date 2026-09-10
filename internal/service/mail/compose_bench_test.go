package mail_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// oneMebibyte is the attachment size the report quotes a per-megabyte cost
// from. Base64 expands by 4/3 plus the line breaks, so the interesting number
// is not the absolute time but the ratio and the cost per unit of payload.
const oneMebibyte int = 1 << 20

// oneMebibyteFloat is the same value as a divisor, so the expansion metric does
// not convert on every iteration.
const oneMebibyteFloat float64 = 1 << 20

// benchMessage builds the message shape a benchmark exercises.
func benchMessage(kind string) coremail.MessageValue {
	msg := coremail.MessageValue{
		From:    coremail.AddressValue{Name: "Ops", Addr: "ops@fake.example"},
		To:      []coremail.AddressValue{{Addr: "user@fake.example"}},
		Subject: "Quarterly report",
		Text:    strings.Repeat("Please find the numbers attached. ", 20),
	}
	switch kind {
	case "alternative":
		msg.HTML = "<p>" + strings.Repeat("Please find the numbers attached. ", 20) + "</p>"
	case "attachment":
		msg.Attachments = []coremail.AttachmentValue{
			{Filename: "blob.bin", Content: bytes.Repeat([]byte{0xFE}, oneMebibyte)},
		}
	case "simple":
		//: the text-only shape, which the zero message already is.
	default:
		panic("unknown benchmark message kind: " + kind)
	}
	return msg
}

// BenchmarkComposeSimple measures a text/plain message: the whole cost is the
// header block plus one quoted-printable body.
func BenchmarkComposeSimple(b *testing.B) {
	composer := svcmail.NewComposer(svcmail.ComposerConfig{})
	msg := benchMessage("simple")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		raw, err := composer.Compose(msg)
		if err != nil {
			b.Fatalf("Compose: %v", err)
		}
		//: keep the result alive so the compiler cannot elide the work.
		if len(raw) == 0 {
			b.Fatal("empty message")
		}
	}
}

// BenchmarkComposeAlternative measures the multipart/alternative shape: two
// leaves, one container, one boundary.
func BenchmarkComposeAlternative(b *testing.B) {
	composer := svcmail.NewComposer(svcmail.ComposerConfig{})
	msg := benchMessage("alternative")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		raw, err := composer.Compose(msg)
		if err != nil {
			b.Fatalf("Compose: %v", err)
		}
		if len(raw) == 0 {
			b.Fatal("empty message")
		}
	}
}

// BenchmarkComposeAttachment1MiB measures a 1 MiB binary attachment through
// base64. b.SetBytes makes the report quote MB/s directly, which is the
// actionable number: the cost of a mail is dominated by its payload, and the
// expansion ratio decides how much of a server's size limit the payload can
// use.
func BenchmarkComposeAttachment1MiB(b *testing.B) {
	composer := svcmail.NewComposer(svcmail.ComposerConfig{})
	msg := benchMessage("attachment")
	b.SetBytes(int64(oneMebibyte))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		raw, err := composer.Compose(msg)
		if err != nil {
			b.Fatalf("Compose: %v", err)
		}
		b.ReportMetric(float64(len(raw))/oneMebibyteFloat, "expansion")
	}
}

// BenchmarkEncodeHeaderASCII measures the header path when RFC 2047 does
// nothing: the value passes through and only folding is considered.
func BenchmarkEncodeHeaderASCII(b *testing.B) {
	composer := svcmail.NewComposer(svcmail.ComposerConfig{})
	msg := benchMessage("simple")
	msg.Subject = "Invoice 4711 - payment due 30 April"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := composer.Compose(msg); err != nil {
			b.Fatalf("Compose: %v", err)
		}
	}
}

// BenchmarkEncodeHeaderNonASCII measures the same path when every rune must be
// encoded and the result must then be folded across several lines.
func BenchmarkEncodeHeaderNonASCII(b *testing.B) {
	composer := svcmail.NewComposer(svcmail.ComposerConfig{})
	msg := benchMessage("simple")
	msg.Subject = strings.Repeat("Réunion trimestrielle à 9h — ", 8)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := composer.Compose(msg); err != nil {
			b.Fatalf("Compose: %v", err)
		}
	}
}

// BenchmarkMemoryTransportSend measures what a consumer's test suite pays per
// message. It composes, which is the whole point of the double — so this is
// also the cost of the guards.
func BenchmarkMemoryTransportSend(b *testing.B) {
	transport := svcmail.NewMemory()
	msg := benchMessage("simple")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := transport.Send(ctx, msg); err != nil {
			b.Fatalf("Send: %v", err)
		}
		//: the recorder would otherwise grow without bound across iterations.
		transport.Reset()
	}
}

// BenchmarkValidateOnly isolates the guard from the rendering, so the report
// can say what the injection defence costs on its own.
func BenchmarkValidateOnly(b *testing.B) {
	msg := benchMessage("simple")
	msg.Cc = []coremail.AddressValue{{Addr: "cc@fake.example"}}
	msg.Bcc = []coremail.AddressValue{{Addr: "blind@fake.example"}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := coremail.Validate(msg); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
