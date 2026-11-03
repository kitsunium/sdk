package codec

import "github.com/kitsunium/sdk/internal/kernel/concur/snapshot"

// sniffLen is how many bytes of an input Detect reads at most: the 512
// http.DetectContentType reads, so a caller that already peeked that much for
// the standard library passes the same slice.
const sniffLen int = 512

// sniffEntry is one codec Detect asks: the Format it registered under, and its
// Sniffer.
type sniffEntry struct {
	// name is the Format Detect answers when this codec alone claims the bytes.
	name Format
	// sniffer is the codec's Sniffer.
	sniffer Sniffer
}

// sniffers lists, in registration order, every registered codec implementing
// Sniffer. Like the registry it is written once per codec at import and read
// on every Detect: a copy-on-write slice behind one atomic load, so Detect
// takes no lock and allocates nothing.
var sniffers snapshot.Value[[]sniffEntry]

// indexSniffer appends c to Detect's list when it implements Sniffer. Register
// calls it once the Format is c's, so a codec refused for a conflict never
// sniffs.
func indexSniffer(name Format, c Codec) {
	s, ok := c.(Sniffer)
	//: a codec with no recognisable head.
	if !ok {
		return
	}
	//: copy forward and append, then publish the copy atomically.
	sniffers.Update(func(current *[]sniffEntry) *[]sniffEntry {
		var next []sniffEntry
		//: the entries already listed, in their order.
		if current != nil {
			next = append(next, *current...)
		}
		next = append(next, sniffEntry{name: name, sniffer: s})
		//: the new list.
		return &next
	})
}

// Detect returns the Format whose registered codec recognises prefix — the
// first bytes of an input, of which it reads at most 512 as
// http.DetectContentType does — through its Sniffer (#909). It asks every
// codec implementing Sniffer, in registration order, and answers only when
// exactly one claims the bytes: none, or two, is (Format(""), false). It
// allocates nothing.
func Detect(prefix []byte) (f Format, ok bool) {
	//: the head is all a sniffer reads.
	if len(prefix) > sniffLen {
		prefix = prefix[:sniffLen]
	}
	list := sniffers.Load()
	//: no bytes, or no codec that sniffs.
	if len(prefix) == 0 || list == nil {
		//: nothing claims nothing.
		return "", false
	}
	//: every sniffer is asked, so a second claim is seen.
	for _, entry := range *list {
		//: this codec does not recognise the head.
		if !entry.sniffer.Sniff(prefix) {
			continue
		}
		//: a second claim: the bytes are ambiguous.
		if ok {
			//: refused rather than guessed.
			return "", false
		}
		f, ok = entry.name, true
	}
	//: the one claim, or none.
	return f, ok
}
