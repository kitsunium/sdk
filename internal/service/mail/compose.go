// Package mail — the composition helpers: media types, part construction, and
// the size estimate that keeps the output buffer to one allocation.
package mail

import (
	"encoding/base64"
	"mime"
	"path/filepath"
	"strings"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Media types this composer emits for the two body forms.
const (
	typeTextPlain string = "text/plain"
	typeTextHTML  string = "text/html"
)

// mimeVersion is the only value RFC 2045 §4 defines.
const mimeVersion string = "1.0"

// boundaryTokenOctets is the number of random bytes behind every boundary in
// one message. 16 is 128 bits, which is not a probability argument here — see
// [composition.newBoundary] for why a collision is impossible rather than
// unlikely.
const boundaryTokenOctets int = 16

// minBracketedLen is the shortest string that can already carry both angle
// brackets a Message-ID or Content-ID needs.
const minBracketedLen int = 2

// Per-message allowances for [estimateSize]. They are deliberately generous:
// the cost of over-estimating is one larger allocation that is released
// immediately, and the cost of under-estimating is the doubling growth this
// function exists to avoid.
const (
	headerBlockSlack   int = 512
	perAddressOctets   int = 96
	perAttachmentSlack int = 384
	subjectExpansion   int = 4
	// quotedPrintableSlack is the denominator of the fraction added to a text
	// body: QP expands ASCII by about 2 % and non-ASCII by three, so a quarter
	// is a middle that is wrong cheaply in both directions.
	quotedPrintableSlack int = 4
	// crlfOctets is the two-octet line terminator every wrapped base64 line
	// costs on top of its content.
	crlfOctets int = 2
)

// bracket wraps an identifier in the angle brackets RFC 5322 §3.6.4 requires,
// tolerating a caller who already wrote them.
func bracket(id string) string {
	//: already bracketed by the caller.
	if len(id) >= minBracketedLen && id[0] == '<' && id[len(id)-1] == '>' {
		//: unchanged.
		return id
	}
	//: bracketed here.
	return "<" + id + ">"
}

// splitAttachments separates the inline parts from the file attachments,
// preserving the caller's relative order within each group.
func splitAttachments(all []coremail.AttachmentValue) (inlines, files []coremail.AttachmentValue) {
	//: one pass, two slices, no map and no sort.
	for index := range all {
		//: one field decides which container the part lands in.
		if all[index].Inline() {
			inlines = append(inlines, all[index])
			//: next.
			continue
		}
		files = append(files, all[index])
	}
	//: grouped.
	return inlines, files
}

// textEntity builds a text leaf. An empty body still produces a part, because
// the alternative is a message whose body a client cannot find.
func textEntity(mediaType, content string) entity {
	//: charset is not optional: without it RFC 2045 §5.2 makes the default
	//: us-ascii, and every accented character in the body becomes a question
	//: mark on a receiver that believes the header over the bytes.
	rendered, formatErr := formatMediaType(mediaType, map[string]string{"charset": "utf-8"})
	//: the only inputs here are this package's own constants, so the error
	//: cannot fire; falling back keeps the type total rather than adding a
	//: second error path to every caller.
	if formatErr != nil {
		//: the literal form of the same value.
		rendered = mediaType + "; charset=utf-8"
	}
	//: quoted-printable, always — see entity.writeBody.
	return entity{contentType: rendered, encoding: encodingQuotedPrintable, content: []byte(content), text: true}
}

// attachmentEntity builds a file or inline leaf.
func attachmentEntity(attachment *coremail.AttachmentValue, index int) (part entity, err error) {
	declared := attachment.ContentType
	//: an undeclared type is derived from the extension, and from nothing else.
	//: Sniffing the bytes is what decides an uploaded .txt is really HTML.
	if declared == "" {
		//: mime.TypeByExtension, then the RFC 2046 §4.5.1 fallback.
		declared = derivedType(attachment.Filename)
	}
	//: RFC 2183 §2.3 deprecates Content-Type's "name" in favour of
	//: Content-Disposition's "filename", and receivers in the field still read
	//: it — both are rendered from ONE value, so they cannot disagree.
	contentType, typeErr := formatMediaType(declared, map[string]string{"name": attachment.Filename})
	//: ComposeFailed.
	if typeErr != nil {
		//: the position is diagnostic.
		return entity{}, errs.Wrap(ComposeFailed, errs.WrapParams{}, errs.Int("attachment", index))
	}
	disposition := "attachment"
	//: one field decides both the disposition and the container.
	if attachment.Inline() {
		//: RFC 2183 §2.2.
		disposition = "inline"
	}
	rendered, dispErr := formatMediaType(disposition, map[string]string{"filename": attachment.Filename})
	//: ComposeFailed.
	if dispErr != nil {
		//: the position is diagnostic.
		return entity{}, errs.Wrap(ComposeFailed, errs.WrapParams{}, errs.Int("attachment", index))
	}
	leaf := entity{contentType: contentType, encoding: encodingBase64, disposition: rendered, content: attachment.Content}
	//: an inline part needs the identifier its cid: URL resolves against.
	if attachment.Inline() {
		//: RFC 2045 §7 wants the angle brackets; RFC 2392 §2's cid: does not.
		leaf.contentID = bracket(attachment.ContentID)
	}
	//: renderable.
	return leaf, nil
}

// derivedType maps a filename extension to a media type, falling back to
// RFC 2046 §4.5.1's application/octet-stream.
func derivedType(filename string) string {
	//: the extension is the only evidence used, on purpose.
	if guessed := mime.TypeByExtension(filepath.Ext(filename)); guessed != "" {
		//: a registered type for this extension.
		return guessed
	}
	//: "arbitrary binary data" (RFC 2046 §4.5.1).
	return coremail.DefaultAttachmentType
}

// formatMediaType renders a media type with parameters, refusing what
// mime.FormatMediaType cannot spell.
//
// mime.FormatMediaType returns "" on invalid input rather than an error, which
// is exactly the silence this domain refuses: an unspellable Content-Type would
// become an empty header and the message would go out untyped.
func formatMediaType(mediaType string, params map[string]string) (rendered string, err error) {
	//: an empty result is the stdlib's way of saying "I cannot".
	if formatted := mime.FormatMediaType(mediaType, params); formatted != "" {
		//: spellable, RFC 2231 continuation and all.
		return formatted, nil
	}
	//: refused rather than emitted empty.
	return "", errs.Wrap(ComposeFailed, errs.WrapParams{}, errs.String("media_type", mediaType))
}

// mediaTypeOnly strips the parameters from a rendered Content-Type value,
// leaving the "type/subtype" a multipart/related "type" parameter needs.
func mediaTypeOnly(contentType string) string {
	//: everything up to the first parameter separator; Cut returns the whole
	//: string when there is none.
	bare, _, _ := strings.Cut(contentType, ";")
	//: the bare media type.
	return bare
}

// estimateSize returns an upper-ish bound on the composed message, so the
// output buffer is allocated once instead of doubled.
//
// It is a measured optimisation and not a guess. Before it, a 1 MiB attachment
// allocated 4.20 MB to produce a 1.44 MB message — 2.9x the result — and
// bytes.growSlice was 43.7 % of all bytes allocated by a simple compose
// (go tool pprof -top -sample_index=alloc_space). The base64 half of the
// estimate is EXACT, because RFC 2045 §6.8 fixes both the 4/3 expansion and the
// 76-octet line width; only the text half is approximate, and it is the half
// that is small.
func estimateSize(msg coremail.MessageValue) int {
	total := headerBlockSlack + subjectExpansion*len(msg.Subject)
	//: every mailbox reaches a header as a name-addr, folded.
	total += perAddressOctets * (len(msg.To) + len(msg.Cc) + len(msg.ReplyTo) + len(msg.Headers) + 1)
	//: the two text bodies, each with its quoted-printable slack.
	total += len(msg.Text) + len(msg.Text)/quotedPrintableSlack
	total += len(msg.HTML) + len(msg.HTML)/quotedPrintableSlack
	//: base64 is exactly computable, and it is the term that dominates.
	for index := range msg.Attachments {
		encoded := base64.StdEncoding.EncodedLen(len(msg.Attachments[index].Content))
		//: one CRLF for every full line, plus one for the short last line.
		total += encoded + crlfOctets*(encoded/base64LineOctets+1) + perAttachmentSlack
	}
	//: one allocation's worth.
	return total
}
